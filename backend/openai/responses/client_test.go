package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func generate(client *Client, ctx context.Context, request agent.Request, onText, onReasoning agent.TextSink, onToolCall agent.ToolCallSink) (agent.Response, error) {
	return client.Generate(ctx, request, agent.StreamObserver{Text: onText, Reasoning: onReasoning, ToolCall: onToolCall})
}

func TestClientResponsesRoundTripAndRawReplay(t *testing.T) {
	const token = "secret-test-token"
	var requestNumber atomic.Int32
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := int(requestNumber.Add(1))
		if request.Method != http.MethodPost || request.URL.Path != "/codex/responses" {
			t.Errorf("request %d = %s %s", call, request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request %d headers = %v", call, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request %d: %v", call, err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(string(body), token) {
			t.Errorf("request %d body contains OAuth token", call)
		}
		if strings.Contains(string(body), "not sent") {
			t.Errorf("request %d leaked prompt-only tool metadata: %s", call, body)
		}
		var rawRequest map[string]json.RawMessage
		if err := json.Unmarshal(body, &rawRequest); err != nil {
			t.Errorf("decode raw request %d: %v", call, err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, field := range []string{"model", "instructions", "input", "tools", "store", "stream", "include", "reasoning"} {
			if _, exists := rawRequest[field]; !exists {
				t.Errorf("request %d missing field %q: %s", call, field, body)
			}
		}
		if _, exists := rawRequest["previous_response_id"]; exists {
			t.Errorf("request %d sent previous_response_id", call)
		}

		var wire createResponseRequest
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Errorf("decode request %d: %v", call, err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if wire.Model != "test-model" || wire.Instructions != "system instructions" || wire.Store || !wire.Stream || !slices.Equal(wire.Include, []string{"reasoning.encrypted_content"}) || wire.Reasoning == nil || wire.Reasoning.Effort != "high" || wire.Reasoning.Summary != "auto" {
			t.Errorf("request %d shape = %+v", call, wire)
		}
		if len(wire.Tools) != 2 || wire.Tools[0].Type != "function" || wire.Tools[0].Name != "read" || wire.Tools[1].Name != "bash" || !wire.Tools[0].Strict || !wire.Tools[1].Strict || wire.Tools[0].Parameters.Type != "object" || wire.Tools[0].Parameters.AdditionalProperties == nil || *wire.Tools[0].Parameters.AdditionalProperties || !slices.Equal(wire.Tools[0].Parameters.Required, []string{"path"}) {
			t.Errorf("request %d tools = %+v", call, wire.Tools)
		}

		switch call {
		case 1:
			assertInputItem(t, wire.Input, 0, map[string]string{"role": "user", "content": "inspect"})
			writeJSON(t, writer, map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{"id": "rs_1", "type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-ciphertext", "status": "completed"},
					map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "phase": "commentary", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Inspecting."}}},
					map[string]any{"id": "fc_item_1", "type": "function_call", "call_id": "call_read", "name": "read", "arguments": `{"path":"file.go"}`, "status": "completed"},
					map[string]any{"id": "future_1", "type": "future_state", "future": map[string]any{"kept": true}},
					map[string]any{"id": "fc_item_2", "type": "function_call", "call_id": "call_bash", "name": "bash", "arguments": `{"command":"go test ./..."}`, "status": "completed"},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14, "output_tokens_details": map[string]any{"reasoning_tokens": 2}},
			})
		case 2:
			if len(wire.Input) != 8 {
				t.Errorf("second request input count = %d, want 8", len(wire.Input))
			}
			assertInputItem(t, wire.Input, 0, map[string]string{"role": "user", "content": "inspect"})
			assertInputItem(t, wire.Input, 1, map[string]string{"type": "reasoning", "encrypted_content": "opaque-ciphertext"})
			assertInputItem(t, wire.Input, 2, map[string]string{"type": "message", "phase": "commentary"})
			assertInputItem(t, wire.Input, 4, map[string]string{"type": "future_state"})
			var futureItem struct {
				Future map[string]bool `json:"future"`
			}
			if err := json.Unmarshal(wire.Input[4], &futureItem); err != nil || !futureItem.Future["kept"] {
				t.Errorf("unknown output fields were not replayed: %s, error=%v", wire.Input[4], err)
			}
			assertInputItem(t, wire.Input, 6, map[string]string{"type": "function_call_output", "call_id": "call_read", "output": "file contents"})
			assertInputItem(t, wire.Input, 7, map[string]string{"type": "function_call_output", "call_id": "call_bash", "output": "[tool error]\nexit status 1"})
			writeJSON(t, writer, map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{"id": "msg_2", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Fixed."}}},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23},
			})
		case 3:
			if len(wire.Input) != 10 {
				t.Errorf("third request input count = %d, want 10", len(wire.Input))
			}
			assertInputItem(t, wire.Input, 8, map[string]string{"type": "message", "phase": "final_answer"})
			assertInputItem(t, wire.Input, 9, map[string]string{"role": "user", "content": "next"})
			writeJSON(t, writer, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Next answer."}}}},
			})
		default:
			t.Errorf("unexpected request %d", call)
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client := newTestClient(t, token, server.URL, Options{})
	tools := []agent.ToolDefinition{strictTestTool("read"), strictTestTool("bash")}
	var sinkText []string
	first, err := generate(client, context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "system instructions",
		Inputs:        []agent.Input{agent.NewTextInput("inspect")},
		Tools:         tools,
	}, func(text string) error {
		sinkText = append(sinkText, text)
		return nil
	}, nil, nil)
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	if first.Text != "Inspecting." || !slices.Equal(sinkText, []string{"Inspecting."}) {
		t.Fatalf("first text = %q, sink = %v", first.Text, sinkText)
	}
	if first.Usage != (agent.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}) {
		t.Fatalf("first usage = %+v", first.Usage)
	}
	if len(first.ToolCalls) != 2 || first.ToolCalls[0].ID != "call_read" || first.ToolCalls[0].Name != "read" || string(first.ToolCalls[0].Arguments) != `{"path":"file.go"}` || first.ToolCalls[1].ID != "call_bash" {
		t.Fatalf("first tool calls = %+v", first.ToolCalls)
	}
	firstState, err := decodeState(first.State, defaultMaxStateBytes)
	if err != nil || len(firstState) != 6 {
		t.Fatalf("first state items = %d, error = %v", len(firstState), err)
	}

	second, err := generate(client, context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "system instructions",
		State:         first.State,
		Tools:         tools,
		Inputs: []agent.Input{
			{Kind: agent.InputToolResult, CallID: "call_read", Tool: "read", Text: "file contents"},
			{Kind: agent.InputToolResult, CallID: "call_bash", Tool: "bash", Text: "exit status 1", IsError: true},
		},
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Text != "Fixed." || second.Usage != (agent.Usage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23}) {
		t.Fatalf("second response = %+v", second)
	}

	third, err := generate(client, context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "system instructions",
		State:         second.State,
		Tools:         tools,
		Inputs:        []agent.Input{agent.NewTextInput("next")},
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("third Generate() error = %v", err)
	}
	if third.Text != "Next answer." || requestNumber.Load() != 3 {
		t.Fatalf("third response = %+v, requests = %d", third, requestNumber.Load())
	}
}

func TestClientRejectsOversizedBodiesAndRequests(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, strings.Repeat("x", 101))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		client.maxResponseBytes = 100
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if err == nil {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("request", func(t *testing.T) {
		var calls atomic.Int32
		server := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		client.maxRequestBytes = 100
		request := baseRequest()
		request.Inputs[0].Content[0].Text = strings.Repeat("x", 200)
		_, err := generate(client, context.Background(), request, nil, nil, nil)
		if err == nil || calls.Load() != 0 {
			t.Fatalf("Generate() error = %v, HTTP calls = %d", err, calls.Load())
		}
	})
}

func TestClientRejectsOversizedReturnedStateBeforeTextSink(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}],"unknown":"`+strings.Repeat("x", 200)+`"}]}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	client.maxStateBytes = 100
	client.stateOutputHeadroom = 1
	sinkCalled := false
	_, err := generate(client, context.Background(), baseRequest(), func(string) error {
		sinkCalled = true
		return nil
	}, nil, nil)
	if err == nil || sinkCalled {
		t.Fatalf("Generate() error = %v, sink called = %v", err, sinkCalled)
	}
}

func TestClientCancellationTimeoutSinkAndRedirect(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		client := newTestClient(t, "key", "http://127.0.0.1:1", Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := generate(client, ctx, baseRequest(), nil, nil, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("HTTP timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-release
		}))
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: &http.Client{Timeout: 30 * time.Millisecond}})
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		close(release)
		server.Close()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("body timeout", func(t *testing.T) {
		transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errorReadCloser{err: context.DeadlineExceeded}}, nil
		})
		client, err := New(Options{PrepareRequest: testPrepareRequest("token"), RequestOptions: testRequestOptions, Endpoint: "https://example.com/responses", HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("sink", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		sinkError := errors.New("sink failed")
		_, err := generate(client, context.Background(), baseRequest(), func(string) error { return sinkError }, nil, nil)
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("streaming sink", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		sinkError := errors.New("streaming sink failed")
		_, err := generate(client, context.Background(), baseRequest(), func(string) error { return sinkError }, nil, nil)
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("stream cancellation", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		seen := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() {
			_, err := generate(client, ctx, baseRequest(), func(string) error { seen <- struct{}{}; return nil }, nil, nil)
			done <- err
		}()
		select {
		case <-seen:
			cancel()
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("stream delta was not delivered")
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var destinationCalls atomic.Int32
		destination := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
		defer destination.Close()
		origin := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		client := newTestClient(t, "key", origin.URL, Options{})
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		var responseErr *httpResponseError
		if !errors.As(err, &responseErr) || responseErr.statusCode != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
			t.Fatalf("Generate() error = %v, destination calls = %d", err, destinationCalls.Load())
		}
	})
}

func TestClientValidatesRequestsBeforePreparingRequest(t *testing.T) {
	var prepareCalls atomic.Int32
	options := testOptions("", "http://127.0.0.1:1")
	options.PrepareRequest = func(context.Context, *http.Request) error {
		prepareCalls.Add(1)
		return nil
	}
	client, err := New(options)
	if err != nil {
		t.Fatal(err)
	}

	invalidState := baseRequest()
	invalidState.State = []byte(`not-json`)
	if _, err := client.Generate(context.Background(), invalidState, agent.StreamObserver{}); err == nil {
		t.Fatal("Generate() accepted invalid state")
	}
	if _, err := client.Compact(context.Background(), invalidState); err == nil {
		t.Fatal("Compact() accepted invalid state")
	}

	client.maxRequestBytes = 1
	if _, err := client.Generate(context.Background(), baseRequest(), agent.StreamObserver{}); err == nil {
		t.Fatal("Generate() accepted oversized request")
	}
	if prepareCalls.Load() != 0 {
		t.Fatalf("prepare request calls = %d", prepareCalls.Load())
	}
}

func TestNewCopiesInjectedClientBeforeApplyingPolicy(t *testing.T) {
	redirect := func(*http.Request, []*http.Request) error { return nil }
	transport := http.DefaultTransport
	injected := &http.Client{Transport: transport, CheckRedirect: redirect}

	client, err := New(Options{
		Endpoint:       "https://example.com/responses",
		HTTPClient:     injected,
		RequestOptions: testRequestOptions,
		PrepareRequest: testPrepareRequest("token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient == injected || client.httpClient.Transport != transport || client.httpClient.Timeout != defaultHTTPTimeout {
		t.Fatalf("owned client=%p injected=%p transport=%T timeout=%s", client.httpClient, injected, client.httpClient.Transport, client.httpClient.Timeout)
	}
	if injected.Timeout != 0 || injected.CheckRedirect == nil {
		t.Fatalf("injected client was mutated: timeout=%s redirect missing=%t", injected.Timeout, injected.CheckRedirect == nil)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err := client.httpClient.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("owned redirect policy error = %v", err)
	}
	if err := injected.CheckRedirect(request, nil); err != nil {
		t.Fatalf("injected redirect policy changed: %v", err)
	}
}

func newTestClient(t *testing.T, token, baseURL string, overrides Options) *Client {
	t.Helper()
	if overrides.Endpoint == "" {
		overrides.Endpoint = strings.TrimRight(baseURL, "/") + "/codex/responses"
	}
	if overrides.PrepareRequest == nil {
		overrides.PrepareRequest = testPrepareRequest(token)
	}
	if overrides.RequestOptions == nil {
		overrides.RequestOptions = testRequestOptions
	}
	client, err := New(overrides)
	if err != nil {
		t.Fatal(err)
	}

	return client
}

func testOptions(token, endpoint string) Options {
	return Options{Endpoint: endpoint, PrepareRequest: testPrepareRequest(token), RequestOptions: testRequestOptions}
}

func testPrepareRequest(token string) PrepareRequestFunc {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("chatgpt-account-id", "account")
		request.Header.Set("originator", "eul")
		request.Header.Set("User-Agent", "eul")
		request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
		request.Header.Set("OpenAI-Beta", "responses=experimental")
		return nil
	}
}

func testRequestOptions(request agent.Request) (RequestOptions, error) {
	level := request.ThinkingLevel
	if level == "" {
		level = agent.DefaultThinkingLevel
	}
	effort := map[agent.ThinkingLevel]string{
		agent.ThinkingOff: "none", agent.ThinkingMinimal: "minimal", agent.ThinkingLow: "low",
		agent.ThinkingMedium: "medium", agent.ThinkingHigh: "high", agent.ThinkingXHigh: "xhigh", agent.ThinkingMax: "max",
	}[level]
	summary := "auto"
	if level == agent.ThinkingOff {
		summary = ""
	}
	serviceTier := ""
	if request.FastMode {
		serviceTier = "priority"
	}
	return RequestOptions{
		Reasoning: &Reasoning{Effort: effort, Summary: summary}, ServiceTier: serviceTier,
		TextVerbosity: "low", Include: []string{"reasoning.encrypted_content"}, ToolChoice: "auto", ParallelToolCalls: true,
	}, nil
}

func strictTestTool(name string) agent.ToolDefinition {
	additionalProperties := false
	property := "path"
	if name == "bash" {
		property = "command"
	}

	return agent.ToolDefinition{
		Name:        name,
		Description: "Read a file",
		Parameters: agent.JSONSchema{
			Type:                 "object",
			Properties:           map[string]agent.JSONSchema{property: {Type: "string"}},
			Required:             []string{property},
			AdditionalProperties: &additionalProperties,
		},
	}
}

func baseRequest() agent.Request {
	return agent.Request{
		Model:        "test-model",
		Instructions: "instructions",
		Inputs:       []agent.Input{agent.NewTextInput("hello")},
		Tools:        []agent.ToolDefinition{strictTestTool("read")},
	}
}

func responseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if status >= 200 && status < 300 {
			payload := body
			var compact bytes.Buffer
			if json.Compact(&compact, []byte(body)) == nil {
				payload = compact.String()
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(status)
			_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		if _, err := io.WriteString(writer, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
}

func compactResponseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if status < 200 || status >= 300 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			if _, err := io.WriteString(writer, body); err != nil {
				t.Errorf("write compact response: %v", err)
			}
			return
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(status)
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
	}))
}

func writeCompactSSE(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(value); err != nil {
		t.Errorf("encode compact response: %v", err)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", bytes.TrimSpace(payload.Bytes()))
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", bytes.TrimSpace(payload.Bytes()))
}

func assertInputItem(t *testing.T, items []json.RawMessage, index int, fields map[string]string) {
	t.Helper()
	if index >= len(items) {
		t.Errorf("input item %d missing from %d items", index, len(items))
		return
	}

	var item map[string]any
	if err := json.Unmarshal(items[index], &item); err != nil {
		t.Errorf("decode input item %d: %v", index, err)
		return
	}

	for name, want := range fields {
		got, _ := item[name].(string)
		if name == "content" && got == "" {
			parts, _ := item[name].([]any)
			if len(parts) == 1 {
				part, _ := parts[0].(map[string]any)
				got, _ = part["text"].(string)
			}
		}
		if got != want {
			t.Errorf("input item %d field %q = %q, want %q; item=%s", index, name, got, want, items[index])
		}
	}
}

type emptyToolbox struct{}

func (emptyToolbox) Definitions() []agent.ToolDefinition { return nil }
func (emptyToolbox) Presentation(agent.ToolCallSnapshot) agent.ToolPresentation {
	return agent.ToolPresentation{}
}

func (emptyToolbox) Execute(context.Context, agent.ToolCall, agent.ToolUpdateSink) (agent.ToolResult, error) {
	return agent.ToolResult{}, errors.New("unexpected tool call")
}

type errorReadCloser struct {
	err error
}

func (reader errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (errorReadCloser) Close() error                    { return nil }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
