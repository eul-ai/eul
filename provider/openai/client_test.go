package openai

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

	"yaah/agent"
)

func TestClientResponsesRoundTripAndRawReplay(t *testing.T) {
	const apiKey = "secret-test-key"
	var requestNumber atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := int(requestNumber.Add(1))
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			t.Errorf("request %d = %s %s", call, request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+apiKey || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request %d headers = %v", call, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request %d: %v", call, err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(string(body), apiKey) {
			t.Errorf("request %d body contains API key", call)
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
		if len(wire.Tools) != 2 || wire.Tools[0].Type != "function" || wire.Tools[0].Name != "read" || wire.Tools[1].Name != "bash" || wire.Tools[0].Strict == nil || !*wire.Tools[0].Strict || wire.Tools[1].Strict == nil || !*wire.Tools[1].Strict || wire.Tools[0].Parameters.Type != "object" || wire.Tools[0].Parameters.AdditionalProperties == nil || *wire.Tools[0].Parameters.AdditionalProperties || !slices.Equal(wire.Tools[0].Parameters.Required, []string{"path"}) {
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

	client := newTestClient(t, apiKey, server.URL, Options{ReasoningEffort: "high"})
	tools := []agent.ToolDefinition{strictTestTool("read"), strictTestTool("bash")}
	var sinkText []string
	first, err := client.Generate(context.Background(), agent.Request{
		Model:        "test-model",
		Instructions: "system instructions",
		Inputs:       []agent.Input{{Kind: agent.InputUser, Text: "inspect"}},
		Tools:        tools,
	}, func(text string) error {
		sinkText = append(sinkText, text)
		return nil
	})
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

	second, err := client.Generate(context.Background(), agent.Request{
		Model:        "test-model",
		Instructions: "system instructions",
		State:        first.State,
		Tools:        tools,
		Inputs: []agent.Input{
			{Kind: agent.InputToolResult, CallID: "call_read", Tool: "read", Text: "file contents"},
			{Kind: agent.InputToolResult, CallID: "call_bash", Tool: "bash", Text: "exit status 1", IsError: true},
		},
	}, nil)
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Text != "Fixed." || second.Usage != (agent.Usage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23}) {
		t.Fatalf("second response = %+v", second)
	}

	third, err := client.Generate(context.Background(), agent.Request{
		Model:        "test-model",
		Instructions: "system instructions",
		State:        second.State,
		Tools:        tools,
		Inputs:       []agent.Input{{Kind: agent.InputUser, Text: "next"}},
	}, nil)
	if err != nil {
		t.Fatalf("third Generate() error = %v", err)
	}
	if third.Text != "Next answer." || requestNumber.Load() != 3 {
		t.Fatalf("third response = %+v, requests = %d", third, requestNumber.Load())
	}
}

func TestPlatformClientStreamsTextDeltas(t *testing.T) {
	releaseTerminal := make(chan struct{})
	released := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if !wire.Stream || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream=%v accept=%q", wire.Stream, request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"streamed \"}\n\n")
		writer.(http.Flusher).Flush()
		<-releaseTerminal
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"answer\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"streamed answer\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	defer func() {
		if !released {
			close(releaseTerminal)
		}
	}()
	client := newTestClient(t, "key", server.URL, Options{})
	var deltas []string
	seenDelta := make(chan string, 2)
	type outcome struct {
		response agent.Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := client.Generate(context.Background(), baseRequest(), func(delta string) error {
			deltas = append(deltas, delta)
			seenDelta <- delta
			return nil
		})
		done <- outcome{response: response, err: err}
	}()
	select {
	case delta := <-seenDelta:
		if delta != "streamed " {
			t.Fatalf("first delta = %q", delta)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first delta was not delivered before the terminal event")
	}
	close(releaseTerminal)
	released = true
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.response.Text != "streamed answer" || !slices.Equal(deltas, []string{"streamed ", "answer"}) {
		t.Fatalf("response=%+v deltas=%q", result.response, deltas)
	}
}

func TestPlatformClientStreamsRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.refusal.delta\",\"delta\":\"Cannot comply.\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"Cannot comply.\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	var delivered string
	response, err := client.Generate(context.Background(), baseRequest(), func(delta string) error { delivered += delta; return nil })
	if err != nil || response.Text != "Cannot comply." || delivered != response.Text {
		t.Fatalf("response=%+v delivered=%q error=%v", response, delivered, err)
	}
}

func TestClientDecodesRefusalAndPreservesMalformedToolArguments(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{
		"status":"completed",
		"output":[
			{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply."}]},
			{"type":"function_call","id":"fc_item","call_id":"call_1","name":"read","arguments":"{bad"}
		]
	}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	response, err := client.Generate(context.Background(), baseRequest(), nil)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Text != "Cannot comply." || len(response.ToolCalls) != 1 || string(response.ToolCalls[0].Arguments) != "{bad" {
		t.Fatalf("response = %+v", response)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed JSON", body: `{`, want: "decode Responses SSE"},
		{name: "trailing JSON", body: `{"status":"completed","output":[]} {}`, want: "decode Responses SSE"},
		{name: "incomplete", body: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, want: "incomplete: max_output_tokens"},
		{name: "response error", body: `{"status":"failed","error":{"type":"server_error","code":"bad","message":"failed"},"output":[]}`, want: "server_error/bad: failed"},
		{name: "missing call ID", body: `{"status":"completed","output":[{"type":"function_call","name":"read","arguments":"{}"}]}`, want: "no call ID"},
		{name: "missing call name", body: `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","arguments":"{}"}]}`, want: "no name"},
		{name: "duplicate call ID", body: `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{}"}]}`, want: "duplicate function call ID"},
		{name: "negative usage", body: `{"status":"completed","output":[],"usage":{"input_tokens":-1,"output_tokens":0,"total_tokens":0}}`, want: "negative token"},
		{name: "non-object output", body: `{"status":"completed","output":[3]}`, want: "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := responseServer(t, http.StatusOK, test.body)
			defer server.Close()
			client := newTestClient(t, "key", server.URL, Options{})
			_, err := client.Generate(context.Background(), baseRequest(), func(string) error {
				t.Fatal("text sink called for malformed response")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestClientBoundsHTTPErrors(t *testing.T) {
	const key = "top-secret-key"
	server := responseServer(t, http.StatusBadRequest, strings.Repeat(key+" ", 100))
	defer server.Close()
	client := newTestClient(t, key, server.URL, Options{MaxErrorBytes: 160})
	_, err := client.Generate(context.Background(), baseRequest(), nil)
	if err == nil {
		t.Fatal("Generate() succeeded")
	}
	if len(err.Error()) > 160 || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("bounded error = %q (%d bytes)", err, len(err.Error()))
	}
}

func TestClientParsesStructuredHTTPError(t *testing.T) {
	server := responseServer(t, http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","code":"rate_limit","message":"slow down"}}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	_, err := client.Generate(context.Background(), baseRequest(), nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "rate_limit_error/rate_limit: slow down") {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestClientRejectsOversizedBodiesAndRequests(t *testing.T) {
	t.Run("response", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, strings.Repeat("x", 101))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{MaxResponseBytes: 100})
		_, err := client.Generate(context.Background(), baseRequest(), nil)
		if err == nil || !strings.Contains(err.Error(), "response exceeds 100 bytes") {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("request", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{MaxRequestBytes: 100})
		request := baseRequest()
		request.Inputs[0].Text = strings.Repeat("x", 200)
		_, err := client.Generate(context.Background(), request, nil)
		if err == nil || !strings.Contains(err.Error(), "request exceeds 100 bytes") || calls.Load() != 0 {
			t.Fatalf("Generate() error = %v, HTTP calls = %d", err, calls.Load())
		}
	})
}

func TestClientRejectsOversizedReturnedStateBeforeTextSink(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}],"unknown":"`+strings.Repeat("x", 200)+`"}]}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{MaxStateBytes: 100})
	sinkCalled := false
	_, err := client.Generate(context.Background(), baseRequest(), func(string) error {
		sinkCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "continuation state exceeds 100 bytes") || sinkCalled {
		t.Fatalf("Generate() error = %v, sink called = %v", err, sinkCalled)
	}
}

func TestClientCancellationTimeoutSinkAndRedirect(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		client := newTestClient(t, "key", "http://127.0.0.1:1", Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Generate(ctx, baseRequest(), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("HTTP timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-release
		}))
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: &http.Client{Timeout: 30 * time.Millisecond}})
		_, err := client.Generate(context.Background(), baseRequest(), nil)
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
		client, err := New("key", Options{BaseURL: "https://example.com", HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Generate(context.Background(), baseRequest(), nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("sink", func(t *testing.T) {
		server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		sinkError := errors.New("sink failed")
		_, err := client.Generate(context.Background(), baseRequest(), func(string) error { return sinkError })
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("streaming sink", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		}))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{})
		sinkError := errors.New("streaming sink failed")
		_, err := client.Generate(context.Background(), baseRequest(), func(string) error { return sinkError })
		if !errors.Is(err, sinkError) {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("stream cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
			_, err := client.Generate(ctx, baseRequest(), func(string) error { seen <- struct{}{}; return nil })
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
		destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
		defer destination.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		client := newTestClient(t, "key", origin.URL, Options{})
		_, err := client.Generate(context.Background(), baseRequest(), nil)
		if err == nil || !strings.Contains(err.Error(), "HTTP 307") || destinationCalls.Load() != 0 {
			t.Fatalf("Generate() error = %v, destination calls = %d", err, destinationCalls.Load())
		}
	})
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		options Options
	}{
		{name: "missing key", key: ""},
		{name: "invalid key", key: "bad\nkey"},
		{name: "bad base URL", key: "key", options: Options{BaseURL: "://bad"}},
		{name: "base credentials", key: "key", options: Options{BaseURL: "https://user@example.com"}},
		{name: "plaintext remote base", key: "key", options: Options{BaseURL: "http://example.com"}},
		{name: "invalid reasoning effort", key: "key", options: Options{ReasoningEffort: "extreme"}},
		{name: "negative request limit", key: "key", options: Options{MaxRequestBytes: -1}},
		{name: "negative response limit", key: "key", options: Options{MaxResponseBytes: -1}},
		{name: "negative error limit", key: "key", options: Options{MaxErrorBytes: -1}},
		{name: "negative state limit", key: "key", options: Options{MaxStateBytes: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.key, test.options); err == nil {
				t.Fatalf("New(%q, %+v) succeeded", test.key, test.options)
			}
		})
	}
}

func TestNewCopiesInjectedClientAndAppliesDefaultTimeout(t *testing.T) {
	injected := &http.Client{}
	client, err := New("key", Options{BaseURL: "https://example.com", HTTPClient: injected})
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient == injected || client.httpClient.Timeout != defaultHTTPTimeout || injected.Timeout != 0 {
		t.Fatalf("injected timeout/copy = client:%p %s injected:%p %s", client.httpClient, client.httpClient.Timeout, injected, injected.Timeout)
	}
}

func newTestClient(t *testing.T, key, baseURL string, overrides Options) *Client {
	t.Helper()
	overrides.BaseURL = baseURL
	client, err := New(key, overrides)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func strictTestTool(name string) agent.ToolDefinition {
	additionalProperties := false
	property := "path"
	if name == "bash" {
		property = "command"
	}
	return agent.ToolDefinition{
		Name:          name,
		Description:   "Read a file",
		PromptSummary: "not sent",
		PromptGuidelines: []string{
			"not sent",
		},
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
		Inputs:       []agent.Input{{Kind: agent.InputUser, Text: "hello"}},
		Tools:        []agent.ToolDefinition{strictTestTool("read")},
	}
}

func responseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		if got, _ := item[name].(string); got != want {
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
