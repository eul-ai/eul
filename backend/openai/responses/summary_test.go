package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestClientCompactsBySummarizing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if wire.Instructions != "summarize" || len(wire.Tools) != 0 || wire.ToolChoice != "" || wire.ParallelToolCalls || len(wire.Input) != 3 {
			t.Errorf("summary request = %+v", wire)
		}
		if !strings.Contains(string(wire.Input[0]), "old answer") || !strings.Contains(string(wire.Input[1]), "pending request") {
			t.Errorf("summary input = %s", wire.Input)
		}
		var trigger inputMessage
		if err := json.Unmarshal(wire.Input[2], &trigger); err != nil || trigger.Role != "user" {
			t.Errorf("summary trigger = %s, error = %v", wire.Input[2], err)
		}

		writeJSON(t, writer, map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "  concise handoff  "}},
			}},
			"usage": map[string]any{"input_tokens": 80, "output_tokens": 10, "total_tokens": 90},
		})
	}))
	defer server.Close()

	client := newTestClient(t, "key", server.URL, Options{})
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	compacted, err := client.SemanticCompact(context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingOff,
		Instructions:  "original instructions",
		State:         state,
		Inputs:        []agent.Input{agent.NewTextInput("pending request")},
		Tools:         []agent.ToolDefinition{strictTestTool("read")},
	}, "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if compacted.Usage != (agent.Usage{InputTokens: 80, OutputTokens: 10, TotalTokens: 90}) {
		t.Fatalf("usage = %+v", compacted.Usage)
	}

	items, err := decodeState(compacted.State, defaultMaxStateBytes)
	if err != nil || len(items) != 2 {
		t.Fatalf("summary state items = %d, error = %v", len(items), err)
	}
	var summaryMessage inputMessage
	if err := json.Unmarshal(items[0], &summaryMessage); err != nil || summaryMessage.Role != "assistant" {
		t.Fatalf("summary message = %s, error = %v", items[0], err)
	}
	summaryText, _ := summaryMessage.Content.(string)
	if !strings.Contains(summaryText, "concise handoff") || strings.Contains(summaryText, "<compacted_context>") || strings.Contains(summaryText, "old answer") || strings.Contains(summaryText, "pending request") {
		t.Fatalf("summary text = %q", summaryText)
	}
	var continuationMessage inputMessage
	if err := json.Unmarshal(items[1], &continuationMessage); err != nil || continuationMessage.Role != "user" {
		t.Fatalf("continuation message = %s, error = %v", items[1], err)
	}

	continued, _, err := buildCreateRequest(agent.Request{State: compacted.State}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Input) != 2 {
		t.Fatalf("continued input = %s", continued.Input)
	}
}

func TestManualSemanticCompactionPreservesTurnBoundary(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"concise handoff"}]}]}`)
	defer server.Close()

	client := newTestClient(t, "key", server.URL, Options{})
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := client.SemanticCompact(context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingOff,
		State:         state,
	}, "summarize")
	if err != nil {
		t.Fatal(err)
	}

	continued, _, err := buildCreateRequest(agent.Request{
		State:  compacted.State,
		Inputs: []agent.Input{agent.NewTextInput("new request")},
	}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Input) != 2 {
		t.Fatalf("continued input = %s", continued.Input)
	}
	var summaryMessage, userMessage inputMessage
	if err := json.Unmarshal(continued.Input[0], &summaryMessage); err != nil || summaryMessage.Role != "assistant" {
		t.Fatalf("summary message = %s, error = %v", continued.Input[0], err)
	}
	if err := json.Unmarshal(continued.Input[1], &userMessage); err != nil || userMessage.Role != "user" {
		t.Fatalf("user message = %s, error = %v", continued.Input[1], err)
	}
}

func TestSemanticCompactAcceptsStateThatCannotReserveGenerationOutput(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		var trigger inputMessage
		if len(wire.Input) == 0 {
			t.Error("summary request has no input")
		} else if err := json.Unmarshal(wire.Input[len(wire.Input)-1], &trigger); err != nil || trigger.Role != "user" {
			t.Errorf("summary request did not end with a user turn: %s", wire.Input)
		}
		writeJSON(t, writer, map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "brief"}},
			}},
		})
	}))
	defer server.Close()

	client := newTestClient(t, "key", server.URL, Options{})
	client.maxStateBytes = 600
	client.stateOutputHeadroom = 250
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + strings.Repeat("x", 300) + `"}`)}, client.maxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{Model: "test-model", ThinkingLevel: agent.ThinkingOff, State: state}

	if _, err := client.Generate(context.Background(), request, agent.StreamObserver{}); err == nil || calls != 0 {
		t.Fatalf("Generate() error = %v, calls = %d", err, calls)
	}
	compacted, err := client.SemanticCompact(context.Background(), request, "summarize")
	if err != nil || calls != 1 {
		t.Fatalf("SemanticCompact() response = %+v, error = %v, calls = %d", compacted, err, calls)
	}
	if len(compacted.State) >= len(state) {
		t.Fatalf("compacted state = %d bytes, original = %d", len(compacted.State), len(state))
	}
}
