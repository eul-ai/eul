package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"yaah/agent"
)

type CodexTokenSourceFunc func(context.Context) (CodexCredential, error)

func (function CodexTokenSourceFunc) Token(ctx context.Context) (CodexCredential, error) {
	return function(ctx)
}

func TestCodexClientUsesOAuthEndpointHeadersShapeAndSSE(t *testing.T) {
	const token = "oauth-access-token"
	const accountID = "account-123"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/codex/responses" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		for header, want := range map[string]string{
			"Authorization":      "Bearer " + token,
			"chatgpt-account-id": accountID,
			"originator":         "yaah",
			"User-Agent":         "yaah",
			"OpenAI-Beta":        "responses=experimental",
			"Accept":             "text/event-stream",
			"Content-Type":       "application/json",
		} {
			if got := request.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(body), token) || strings.Contains(string(body), accountID) {
			t.Errorf("request body leaked auth: %s", body)
		}
		var wire createResponseRequest
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Error(err)
		}
		if !wire.Stream || wire.Store || wire.Text == nil || wire.Text.Verbosity != "low" || wire.Reasoning == nil || wire.Reasoning.Effort != "xhigh" || wire.Reasoning.Summary != "auto" || wire.ToolChoice != "auto" || !wire.ParallelToolCalls {
			t.Errorf("Codex request shape = %+v", wire)
		}
		for _, tool := range wire.Tools {
			if tool.Strict != nil {
				t.Errorf("Codex tool strict = %v, want null", *tool.Strict)
			}
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Assessing request\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_part.done\"}\n\n")
		fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"subscription \"}\n\n")
		fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"answer\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"subscription answer\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
	}))
	defer server.Close()

	sourceCalls := 0
	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		sourceCalls++
		return CodexCredential{AccessToken: token, AccountID: accountID}, nil
	}), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	var delivered, reasoning string
	response, err := generate(client, context.Background(), agent.Request{Model: "gpt-5.6-sol", ThinkingLevel: agent.ThinkingXHigh, Inputs: []agent.Input{{Kind: agent.InputUser, Text: "hello"}}, Tools: []agent.ToolDefinition{strictTestTool("read")}}, func(text string) error {
		delivered += text
		return nil
	}, func(text string) error {
		reasoning += text
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "subscription answer" || delivered != response.Text || reasoning != "Assessing request\n\n" || response.Usage.TotalTokens != 5 || sourceCalls != 1 {
		t.Fatalf("response=%+v delivered=%q reasoning=%q sourceCalls=%d", response, delivered, reasoning, sourceCalls)
	}
}

func TestCodexSSEStopsAtTerminalEventWithoutWaitingForEOF(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		writer.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()

	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{AccessToken: "token", AccountID: "account"}, nil
	}), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		response agent.Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
		done <- outcome{response: response, err: err}
	}()
	select {
	case result := <-done:
		close(release)
		if result.err != nil || result.response.Text != "done" {
			t.Fatalf("response=%+v error=%v", result.response, result.err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Generate waited for Codex SSE EOF after terminal event")
	}
}

func TestCodexSSEToolCallAndReasoningReplay(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call++
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Errorf("decode request %d: %v", call, err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1:
			assertInputItem(t, wire.Input, 0, map[string]string{"role": "user", "content": "inspect"})
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_read\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"file.go\\\"}\"}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		case 2:
			if len(wire.Input) != 4 {
				t.Errorf("replayed input count = %d, want 4", len(wire.Input))
			}
			assertInputItem(t, wire.Input, 1, map[string]string{"type": "reasoning", "encrypted_content": "opaque"})
			assertInputItem(t, wire.Input, 2, map[string]string{"type": "function_call", "call_id": "call_read"})
			assertInputItem(t, wire.Input, 3, map[string]string{"type": "function_call_output", "call_id": "call_read", "output": "contents"})
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"finished\"}]}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		default:
			t.Errorf("unexpected request %d", call)
		}
	}))
	defer server.Close()

	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{AccessToken: "token", AccountID: "account"}, nil
	}), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	first, err := generate(client, context.Background(), agent.Request{Model: "model", Inputs: []agent.Input{{Kind: agent.InputUser, Text: "inspect"}}, Tools: []agent.ToolDefinition{strictTestTool("read")}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call_read" || string(first.ToolCalls[0].Arguments) != `{"path":"file.go"}` {
		t.Fatalf("first response = %+v", first)
	}
	second, err := generate(client, context.Background(), agent.Request{Model: "model", State: first.State, Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_read", Tool: "read", Text: "contents"}}, Tools: []agent.ToolDefinition{strictTestTool("read")}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "finished" || call != 2 {
		t.Fatalf("second response=%+v calls=%d", second, call)
	}
}

func TestCodexStreamsPartialToolArgumentsBeforeResponseCompletes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher := writer.(http.Flusher)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_write\",\"name\":\"write\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"path\\\":\\\"demo.go\\\",\\\"content\\\":\\\"pack\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"age main\"}\n\n")
		flusher.Flush()
		<-release
		arguments := `{"path":"demo.go","content":"package main"}`
		encodedArguments, _ := json.Marshal(arguments)
		fmt.Fprintf(writer, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":%s}\n\n", encodedArguments)
		fmt.Fprintf(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_write\",\"name\":\"write\",\"arguments\":%s}}\n\n", encodedArguments)
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{AccessToken: "token", AccountID: "account"}, nil
	}), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	snapshots := make(chan agent.ToolCallSnapshot, 8)
	done := make(chan struct {
		response agent.Response
		err      error
	}, 1)
	go func() {
		response, generateErr := generate(client, context.Background(), baseRequest(), nil, nil, func(snapshot agent.ToolCallSnapshot) error {
			snapshots <- snapshot
			return nil
		})
		done <- struct {
			response agent.Response
			err      error
		}{response: response, err: generateErr}
	}()

	var partial agent.ToolCallSnapshot
	for range 3 {
		select {
		case partial = <-snapshots:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("tool argument snapshot was not delivered before response completion")
		}
	}
	if partial.Complete || partial.ID != "call_write" || !strings.Contains(partial.RawArguments, "package main") {
		close(release)
		t.Fatalf("partial snapshot = %+v", partial)
	}
	close(release)

	result := <-done
	if result.err != nil || len(result.response.ToolCalls) != 1 || string(result.response.ToolCalls[0].Arguments) != `{"path":"demo.go","content":"package main"}` {
		t.Fatalf("response=%+v error=%v", result.response, result.err)
	}
	select {
	case final := <-snapshots:
		if !final.Complete || final.RawArguments != `{"path":"demo.go","content":"package main"}` {
			t.Fatalf("final snapshot = %+v", final)
		}
	default:
		t.Fatal("missing complete tool argument snapshot")
	}
}

func TestResponsesSSECorrelatesInterleavedToolArgumentStreams(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"one","name":"write","arguments":""}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"two","name":"write","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"two.txt\"}"}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"one.txt\"}"}`,
		`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"one.txt\"}"}`,
		`data: {"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"two.txt\"}"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"one","name":"write","arguments":"{\"path\":\"one.txt\"}"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"two","name":"write","arguments":"{\"path\":\"two.txt\"}"}}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\n") + "\n\n"

	var snapshots []agent.ToolCallSnapshot
	response, err := readResponsesSSE(strings.NewReader(body), 1<<20, &streamObserver{observer: agent.StreamObserver{ToolCall: func(snapshot agent.ToolCallSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	text, calls, _, err := normalizeResponse(response)
	if err != nil || text != "" || len(calls) != 2 {
		t.Fatalf("response=%+v calls=%+v error=%v", response, calls, err)
	}
	wantIDs := []string{"one", "two", "two", "one", "one", "two"}
	gotIDs := make([]string, len(snapshots))
	for index, snapshot := range snapshots {
		gotIDs[index] = snapshot.ID
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("snapshot IDs = %v, want %v", gotIDs, wantIDs)
	}
	if !snapshots[4].Complete || !snapshots[5].Complete || snapshots[4].RawArguments != `{"path":"one.txt"}` || snapshots[5].RawArguments != `{"path":"two.txt"}` {
		t.Fatalf("final snapshots = %+v", snapshots[4:])
	}
}

func TestCodexSourceIsResolvedPerRequest(t *testing.T) {
	const first = "first-oauth-secret"
	const second = "refreshed-oauth-secret"
	calls := 0
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		want := first
		if calls == 2 {
			want = second
		}
		if request.Header.Get("Authorization") != "Bearer "+want {
			t.Errorf("request %d authorization = %q", calls, request.Header.Get("Authorization"))
		}
		if calls == 2 {
			return nil, errors.New("transport failed")
		}
		body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	sourceCalls := 0
	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		sourceCalls++
		token := first
		if sourceCalls == 2 {
			token = second
		}
		return CodexCredential{AccessToken: token, AccountID: "account"}, nil
	}), Options{BaseURL: "https://example.com", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generate(client, context.Background(), baseRequest(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err = generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "transport failed") {
		t.Fatalf("second Generate() error = %v", err)
	}
	if sourceCalls != 2 {
		t.Fatalf("source calls = %d", sourceCalls)
	}
}

func TestResponsesSSEValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing terminal", body: "data: {\"type\":\"response.output_text.delta\"}\n\n", want: "without a terminal"},
		{name: "malformed", body: "data: {\n\n", want: "decode Responses SSE"},
		{name: "nested error", body: "data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"message\":\"failed\"}}\n\n", want: "server_error: failed"},
		{name: "top-level error", body: "data: {\"type\":\"error\",\"code\":\"rate_limit\",\"message\":\"slow down\"}\n\n", want: "rate_limit: slow down"},
		{name: "done status default", body: "data: {\"type\":\"response.done\",\"response\":{\"output\":[]}}\n\n", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := readResponsesSSE(strings.NewReader(test.body), 64*1024, nil)
			if test.want == "" {
				if err != nil || response.Status != "completed" {
					t.Fatalf("response=%+v error=%v", response, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
	if _, err := readResponsesSSE(strings.NewReader(strings.Repeat("x", 101)), 100, nil); err == nil || !strings.Contains(err.Error(), "exceeds 100 bytes") {
		t.Fatalf("bounded SSE error = %v", err)
	}
}

func TestCodexDefaultEndpointAndRedirects(t *testing.T) {
	defaultClient, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{AccessToken: "token", AccountID: "account"}, nil
	}), Options{})
	if err != nil || defaultClient.endpoint != "https://chatgpt.com/backend-api/codex/responses" || defaultClient.compactEndpoint != "https://chatgpt.com/backend-api/codex/responses/compact" {
		t.Fatalf("default endpoints=%q %q error=%v", defaultClient.endpoint, defaultClient.compactEndpoint, err)
	}

	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls++ }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{AccessToken: "token", AccountID: "account"}, nil
	}), Options{BaseURL: origin.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || destinationCalls != 0 {
		t.Fatalf("redirect error=%v destination calls=%d", err, destinationCalls)
	}
}

func TestCodexTokenSourceErrorsPropagateWithoutRequest(t *testing.T) {
	sourceError := errors.New("refresh unavailable")
	client, err := NewCodex(CodexTokenSourceFunc(func(context.Context) (CodexCredential, error) {
		return CodexCredential{}, sourceError
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if err == nil || !errors.Is(err, sourceError) || !strings.Contains(err.Error(), sourceError.Error()) {
		t.Fatalf("Generate() error = %v", err)
	}

	started := make(chan struct{})
	client, err = NewCodex(CodexTokenSourceFunc(func(ctx context.Context) (CodexCredential, error) {
		close(started)
		<-ctx.Done()
		return CodexCredential{}, ctx.Err()
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, generateErr := generate(client, ctx, baseRequest(), nil, nil, nil)
		done <- generateErr
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() cancellation error = %v", err)
	}
}
