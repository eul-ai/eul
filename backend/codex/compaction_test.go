package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

func TestProviderCompactsAndReplaysCanonicalState(t *testing.T) {
	const token = "compact-oauth-token"
	var calls int
	httpClient := handlerClient(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var wire struct {
			Model             string            `json:"model"`
			Instructions      string            `json:"instructions"`
			Input             []json.RawMessage `json:"input"`
			Tools             []json.RawMessage `json:"tools"`
			Stream            bool              `json:"stream"`
			ToolChoice        string            `json:"tool_choice"`
			ParallelToolCalls bool              `json:"parallel_tool_calls"`
			Text              *struct {
				Verbosity string `json:"verbosity"`
			} `json:"text"`
			Reasoning *struct {
				Effort  string `json:"effort"`
				Summary string `json:"summary"`
			} `json:"reasoning"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}

		switch calls {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/backend-api/codex/responses" || request.Header.Get("Accept") != "text/event-stream" {
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
			if strings.Contains(string(body), token) {
				t.Fatal("compact request body contains OAuth token")
			}
			if wire.Model != modelMain || wire.Instructions != "system" || !wire.Stream || !wire.ParallelToolCalls || wire.ToolChoice != "auto" || wire.Text == nil || wire.Text.Verbosity != "low" || wire.Reasoning == nil || wire.Reasoning.Effort != "high" || wire.Reasoning.Summary != "auto" || len(wire.Tools) != 1 || len(wire.Input) != 3 {
				t.Errorf("compact request = %s", body)
			}
			assertInputItem(t, wire.Input, 0, map[string]string{"type": "message", "role": "assistant"})
			assertInputItem(t, wire.Input, 1, map[string]string{"role": "user", "content": "pending user"})
			assertInputItem(t, wire.Input, 2, map[string]string{"type": "compaction_trigger"})
			writeCompleted(writer, `{"status":"completed","output":[{"type":"compaction","encrypted_content":"opaque compact state"}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`)
		case 2:
			if len(wire.Input) != 3 {
				t.Fatalf("post-compact input count = %d, want 3", len(wire.Input))
			}
			assertInputItem(t, wire.Input, 0, map[string]string{"role": "user", "content": "pending user"})
			assertInputItem(t, wire.Input, 1, map[string]string{"type": "compaction", "encrypted_content": "opaque compact state"})
			assertInputItem(t, wire.Input, 2, map[string]string{"role": "user", "content": "after compact"})
			writeCompleted(writer, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]}]}`)
		default:
			t.Errorf("unexpected request %d", calls)
		}
	})

	provider := newProvider(testManager(t, token), httpClient)
	state, err := protocol.EncodeState([]json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)})
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := provider.Compact(context.Background(), agent.Request{
		Model:         modelMain,
		ThinkingLevel: agent.ThinkingHigh,
		Instructions:  "system",
		State:         state,
		Inputs:        []agent.Input{agent.NewTextInput("pending user")},
		Tools:         []agent.ToolDefinition{{Name: "read", Parameters: agent.JSONSchema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compacted.Usage != (agent.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}) {
		t.Fatalf("compact usage = %+v", compacted.Usage)
	}
	items, err := protocol.DecodeState(compacted.State)
	if err != nil || len(items) != 2 {
		t.Fatalf("compact state items = %d, error = %v", len(items), err)
	}

	response, err := provider.Generate(context.Background(), agent.Request{
		Model:         modelMain,
		ThinkingLevel: agent.ThinkingHigh,
		State:         compacted.State,
		Inputs:        []agent.Input{agent.NewTextInput("after compact")},
	}, agent.StreamObserver{})
	if err != nil || response.Text != "continued" || calls != 2 {
		t.Fatalf("response = %+v, error = %v, calls = %d", response, err, calls)
	}
}

func TestProviderRejectsMalformedCompactResponses(t *testing.T) {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := handlerClient(func(writer http.ResponseWriter, _ *http.Request) {
				writeCompleted(writer, test.body)
			})
			if _, err := newProvider(testManager(t, "key"), httpClient).Compact(context.Background(), compactRequest()); err == nil {
				t.Fatal("Compact() succeeded")
			}
		})
	}
}

func TestProviderCompactAcceptsMissingUsageAndHonorsCancellation(t *testing.T) {
	httpClient := handlerClient(func(writer http.ResponseWriter, _ *http.Request) {
		writeCompleted(writer, `{"output":[{"type":"compaction","encrypted_content":"opaque"}]}`)
	})
	provider := newProvider(testManager(t, "key"), httpClient)
	response, err := provider.Compact(context.Background(), compactRequest())
	if err != nil || response.Usage != (agent.Usage{}) {
		t.Fatalf("Compact() response = %+v, error = %v", response, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Compact(ctx, compactRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact() error = %v", err)
	}
}

func TestCompactedStateItemsRetainsOnlyGenuineUserMessages(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning"}`),
		json.RawMessage(`{"type":"message","role":"assistant"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"human"}]}`),
		json.RawMessage(`{"role":"user","content":"legacy inbox"}`),
		json.RawMessage(`{"type":"agent_message","content":[{"type":"input_text","text":"inbox"}]}`),
		json.RawMessage(`{"role":"developer","content":[{"type":"input_text","text":"old instructions"}]}`),
		json.RawMessage(`{"role":"system","content":[{"type":"input_text","text":"old system"}]}`),
		json.RawMessage(`{"type":"compaction","encrypted_content":"old"}`),
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"new"}`)}

	items := compactedStateItems(input, output)
	if len(items) != 2 || !strings.Contains(string(items[0]), `"text":"human"`) || !strings.Contains(string(items[1]), `"encrypted_content":"new"`) {
		t.Fatalf("compacted items = %s", items)
	}
}

func TestCompactedStateItemsKeepsNewestUsersWithinBudget(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"oldest message"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"ééé"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"new"}]}`),
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction"}`)}

	items := compactedStateItemsWithBudget(input, output, 2)
	if len(items) != 3 || !strings.Contains(string(items[0]), `"text":"éé"`) || !strings.Contains(string(items[1]), `"text":"new"`) || string(items[2]) != `{"type":"compaction"}` {
		t.Fatalf("compacted items = %s", items)
	}
}

func TestCompactedStateItemsAccountsForImages(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"new"}]}`),
	}

	withoutImage := compactedStateItemsWithBudget(input, nil, retainedImageTokens)
	if len(withoutImage) != 1 || !strings.Contains(string(withoutImage[0]), `"text":"new"`) {
		t.Fatalf("without image = %s", withoutImage)
	}

	withImage := compactedStateItemsWithBudget(input, nil, retainedImageTokens+1)
	if len(withImage) != 2 || !strings.Contains(string(withImage[0]), `"input_image"`) || !strings.Contains(string(withImage[1]), `"text":"new"`) {
		t.Fatalf("with image = %s", withImage)
	}
}

func compactRequest() agent.Request {
	return agent.Request{Model: modelMain, ThinkingLevel: agent.ThinkingMedium, Inputs: []agent.Input{agent.NewTextInput("hello")}}
}

func writeCompleted(writer http.ResponseWriter, response string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
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
		if parts, ok := item[name].([]any); ok && name == "content" && len(parts) == 1 {
			part, _ := parts[0].(map[string]any)
			got, _ = part["text"].(string)
		}
		if got != want {
			t.Errorf("input item %d field %q = %q, want %q; item=%s", index, name, got, want, items[index])
		}
	}
}
