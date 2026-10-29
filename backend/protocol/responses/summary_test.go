package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

func TestClientCompactsBySummarizing(t *testing.T) {
	server := handlerClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if wire.Instructions == "" || wire.Instructions == "original instructions" || len(wire.Tools) != 0 || wire.ToolChoice != "" || wire.ParallelToolCalls || len(wire.Input) != 3 {
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
			"usage": map[string]any{"input_tokens": 80, "output_tokens": 10, "total_tokens": 90, "cost": 0.125},
		})
	}))

	client := newTestClient(t, "key", Options{HTTPClient: server})
	state, err := protocol.EncodeState([]json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)})
	if err != nil {
		t.Fatal(err)
	}

	compacted, err := client.Compact(context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingOff,
		Instructions:  "original instructions",
		State:         state,
		Inputs:        []agent.Input{agent.NewTextInput("pending request")},
		Tools:         []agent.ToolDefinition{strictTestTool("read")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compacted.Usage != (agent.Usage{InputTokens: 80, OutputTokens: 10, TotalTokens: 90}) || compacted.CostUSD != 0.125 {
		t.Fatalf("usage = %+v, cost = %v", compacted.Usage, compacted.CostUSD)
	}

	items, err := protocol.DecodeState(compacted.State)
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

	continued, _, err := testCodec().build(agent.Request{}, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Input) != 2 {
		t.Fatalf("continued input = %s", continued.Input)
	}
}

func TestManualSemanticCompactionPreservesTurnBoundary(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"concise handoff"}]}]}`)

	client := newTestClient(t, "key", Options{HTTPClient: server})
	state, err := protocol.EncodeState([]json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"old answer"}`)})
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model:         "test-model",
		ThinkingLevel: agent.ThinkingOff,
		State:         state,
	})
	if err != nil {
		t.Fatal(err)
	}

	items, err := protocol.DecodeState(compacted.State)
	if err != nil {
		t.Fatal(err)
	}
	continued, _, err := testCodec().build(agent.Request{Inputs: []agent.Input{agent.NewTextInput("new request")}}, items)
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
