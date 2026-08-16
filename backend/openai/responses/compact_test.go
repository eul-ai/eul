package responses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

func TestClientCompactsAndReplaysCanonicalState(t *testing.T) {
	const token = "compact-oauth-token"
	var calls int
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/codex/responses" || request.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("compact request = %s %s accept=%q", request.Method, request.URL.Path, request.Header.Get("Accept"))
			}
			for header, want := range map[string]string{
				"Authorization":         "Bearer " + token,
				"chatgpt-account-id":    "account",
				"originator":            "eul",
				"User-Agent":            "eul",
				"OpenAI-Beta":           "responses=experimental",
				"x-codex-beta-features": "remote_compaction_v2",
				"Content-Type":          "application/json",
			} {
				if got := request.Header.Get(header); got != want {
					t.Errorf("compact header %s = %q, want %q", header, got, want)
				}
			}

			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
			}
			if strings.Contains(string(body), token) {
				t.Fatal("compact request body contains OAuth token")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			var wire createResponseRequest
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Model != "gpt-5.6-sol" || wire.Instructions != "system" || !wire.Stream || !wire.ParallelToolCalls || wire.ToolChoice != "auto" || wire.Text == nil || wire.Text.Verbosity != "low" || wire.Reasoning == nil || wire.Reasoning.Effort != "high" || wire.Reasoning.Summary != "auto" || len(wire.Tools) != 1 || len(wire.Input) != 3 {
				t.Errorf("compact request = %+v", wire)
			}
			assertInputItem(t, wire.Input, 0, map[string]string{"type": "message", "role": "assistant"})
			assertInputItem(t, wire.Input, 1, map[string]string{"role": "user", "content": "pending user"})
			assertInputItem(t, wire.Input, 2, map[string]string{"type": "compaction_trigger"})

			writeCompactSSE(t, writer, map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{"type": "compaction", "encrypted_content": "opaque compact state"},
				},
				"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
			})
		case 2:
			if request.URL.Path != "/codex/responses" {
				t.Errorf("generate path = %q", request.URL.Path)
			}
			var wire createResponseRequest
			if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Input) != 3 {
				t.Fatalf("post-compact input count = %d, want 3", len(wire.Input))
			}
			assertInputItem(t, wire.Input, 0, map[string]string{"role": "user", "content": "pending user"})
			assertInputItem(t, wire.Input, 1, map[string]string{"type": "compaction", "encrypted_content": "opaque compact state"})
			assertInputItem(t, wire.Input, 2, map[string]string{"role": "user", "content": "after compact"})
			writeJSON(t, writer, map[string]any{
				"status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "continued"}}}},
			})
		default:
			t.Errorf("unexpected request %d", calls)
		}
	}))
	defer server.Close()

	client := newTestClient(t, token, server.URL, Options{HTTPClient: server.Client()})
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)})
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "system",
		State:         state,
		Inputs:        []agent.Input{agent.NewTextInput("pending user")},
		Tools:         []agent.ToolDefinition{strictTestTool("read")},
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compacted.Usage != (agent.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}) {
		t.Fatalf("compact usage = %+v", compacted.Usage)
	}
	items, err := continuation.Decode(compacted.State, continuation.DefaultMaximumBytes)
	if err != nil || len(items) != 2 {
		t.Fatalf("compact state items = %d, error = %v", len(items), err)
	}

	response, err := generate(client, context.Background(), agent.Request{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: agent.ThinkingHigh,
		State:         compacted.State,
		Inputs:        []agent.Input{agent.NewTextInput("after compact")},
	}, nil, nil, nil)
	if err != nil || response.Text != "continued" || calls != 2 {
		t.Fatalf("response = %+v, error = %v, calls = %d", response, err, calls)
	}
}

func TestClientRejectsMalformedCompactResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "trailing JSON", body: `{"status":"completed","output":[{}]} {}`},
		{name: "missing output", body: `{"status":"completed"}`},
		{name: "empty output", body: `{"status":"completed","output":[]}`},
		{name: "non-object output", body: `{"status":"completed","output":[3]}`},
		{name: "wrong output type", body: `{"status":"completed","output":[{}]}`},
		{name: "negative usage", body: `{"status":"completed","output":[{"type":"compaction"}],"usage":{"input_tokens":-1}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := compactResponseServer(t, http.StatusOK, test.body)
			defer server.Close()
			client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
			_, err := client.Compact(context.Background(), baseRequest())
			if err == nil {
				t.Fatal("Compact() succeeded")
			}
		})
	}
}

func TestClientCompactBoundsCancellationAndOptionalUsage(t *testing.T) {
	t.Run("optional usage", func(t *testing.T) {
		server := compactResponseServer(t, http.StatusOK, `{"output":[{"type":"compaction","encrypted_content":"opaque"}]}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
		response, err := client.Compact(context.Background(), baseRequest())
		if err != nil || response.Usage != (agent.Usage{}) {
			t.Fatalf("Compact() response = %+v, error = %v", response, err)
		}
	})

	t.Run("response bound", func(t *testing.T) {
		server := compactResponseServer(t, http.StatusOK, strings.Repeat("x", 101))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
		client.maxResponseBytes = 100
		_, err := client.Compact(context.Background(), baseRequest())
		if err == nil {
			t.Fatalf("Compact() error = %v", err)
		}
	})

	t.Run("request bound", func(t *testing.T) {
		var calls atomic.Int32
		server := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
		client.maxRequestBytes = 100
		request := baseRequest()
		request.Inputs[0].Content[0].Text = strings.Repeat("x", 200)
		_, err := client.Compact(context.Background(), request)
		if err == nil || calls.Load() != 0 {
			t.Fatalf("Compact() error = %v, HTTP calls = %d", err, calls.Load())
		}
	})

	t.Run("state bound", func(t *testing.T) {
		server := compactResponseServer(t, http.StatusOK, `{"output":[{"type":"compaction","encrypted_content":"`+strings.Repeat("x", 200)+`"}]}`)
		defer server.Close()
		client := newTestClient(t, "key", server.URL, Options{HTTPClient: server.Client()})
		client.maxStateBytes = 100
		client.stateOutputHeadroom = 1
		_, err := client.Compact(context.Background(), baseRequest())
		if err == nil {
			t.Fatalf("Compact() error = %v", err)
		}
	})

	t.Run("pre-canceled", func(t *testing.T) {
		client := newTestClient(t, "key", "http://127.0.0.1:1", Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Compact(ctx, baseRequest())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Compact() error = %v", err)
		}
	})
}
