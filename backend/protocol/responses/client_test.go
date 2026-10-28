package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/protocol"
)

type Options = protocol.Options

func generate(client *protocol.Client, ctx context.Context, request agent.Request, onText, onReasoning agent.TextSink, onToolCall agent.ToolCallSink) (agent.Response, error) {
	return client.Generate(ctx, request, agent.StreamObserver{Text: onText, Reasoning: onReasoning, ToolCall: onToolCall})
}

func TestClientResponsesRoundTripAndRawReplay(t *testing.T) {
	const token = "secret-test-token"
	var requestNumber atomic.Int32
	server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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

	client := newTestClient(t, token, Options{HTTPClient: server})
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
	firstState, err := protocol.DecodeState(first.State)
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

func TestClientCancellationTimeoutSinkAndRedirect(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		client := newTestClient(t, "key", Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := generate(client, ctx, baseRequest(), nil, nil, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("HTTP timeout", func(t *testing.T) {
		transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
		client := newTestClient(t, "key", Options{HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Millisecond}})
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("body timeout", func(t *testing.T) {
		transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: errorReadCloser{err: context.DeadlineExceeded}}, nil
		})
		client := newTestClient(t, "token", Options{Endpoint: "https://example.com/responses", HTTPClient: &http.Client{Transport: transport}})
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("sink", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
		client := newTestClient(t, "key", Options{HTTPClient: server})
		sinkError := errors.New("sink failed")
		_, err := generate(client, context.Background(), baseRequest(), func(string) error { return sinkError }, nil, nil)
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("streaming sink", func(t *testing.T) {
		server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		}))
		client := newTestClient(t, "key", Options{HTTPClient: server})
		sinkError := errors.New("streaming sink failed")
		_, err := generate(client, context.Background(), baseRequest(), func(string) error { return sinkError }, nil, nil)
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("stream cancellation", func(t *testing.T) {
		server := streamingClient(func(request *http.Request, writer *io.PipeWriter) {
			fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
			<-request.Context().Done()
			writer.CloseWithError(request.Context().Err())
		})
		client := newTestClient(t, "key", Options{HTTPClient: server})
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
		server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Host == "destination.test" {
				destinationCalls.Add(1)
				return
			}
			http.Redirect(writer, request, "https://destination.test/", http.StatusTemporaryRedirect)
		}))
		client := newTestClient(t, "key", Options{HTTPClient: server})
		_, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		var responseErr *backendhttp.APIResponseError
		if !errors.As(err, &responseErr) || responseErr.StatusCode() != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
			t.Fatalf("Generate() error = %v, destination calls = %d", err, destinationCalls.Load())
		}
	})
}

func TestClientValidatesRequestsBeforePreparingRequest(t *testing.T) {
	var prepareCalls atomic.Int32
	client := newTestClient(t, "", Options{PrepareRequest: func(context.Context, *http.Request) error {
		prepareCalls.Add(1)
		return nil
	}})

	invalidState := baseRequest()
	invalidState.State = []byte(`not-json`)
	if _, err := client.Generate(context.Background(), invalidState, agent.StreamObserver{}); err == nil {
		t.Fatal("Generate() accepted invalid state")
	}
	if _, err := client.Compact(context.Background(), invalidState); err == nil {
		t.Fatal("Compact() accepted invalid state")
	}
	if prepareCalls.Load() != 0 {
		t.Fatalf("prepare request calls = %d", prepareCalls.Load())
	}
}

func newTestClient(t *testing.T, token string, overrides Options) *protocol.Client {
	t.Helper()
	if overrides.Endpoint == "" {
		overrides.Endpoint = "https://example.test/codex/responses"
	}
	if overrides.PrepareRequest == nil {
		overrides.PrepareRequest = testPrepareRequest(token)
	}
	overrides.Codec = testCodec()
	overrides.ContextWindow = func(string) int64 { return 0 }
	return protocol.New(overrides)
}

func testCodec() Codec {
	return Codec{Options: testRequestOptions}
}

func testPrepareRequest(token string) func(context.Context, *http.Request) error {
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

func responseServer(t *testing.T, status int, body string) *http.Client {
	t.Helper()
	return handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		_, _ = io.WriteString(writer, body)
	}))
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

type errorReadCloser struct {
	err error
}

func (reader errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (errorReadCloser) Close() error                    { return nil }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
