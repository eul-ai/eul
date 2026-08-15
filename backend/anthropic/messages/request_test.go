package messages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestBuildRequestEncodesAnthropicInputs(t *testing.T) {
	request, history, newMessages, err := buildRequest(agent.Request{
		Model:        "model",
		Instructions: "instructions",
		Inputs: []agent.Input{
			agent.NewToolResultInput(agent.ToolResult{CallID: "call_1", Tool: "read", Output: "failed", IsError: true}),
			agent.NewUserInput(
				agent.ContentPart{Kind: agent.ContentPartText, Text: "look"},
				agent.ContentPart{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
			),
		},
		Tools: []agent.ToolDefinition{{
			Name:        "read",
			Description: "Read a file",
			Parameters:  agent.JSONSchema{Type: "object"},
		}},
	}, defaultMaxStateBytes, defaultMaxStateBytes-defaultStateOutputHeadroom)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != "model" || len(request.System) != 1 || request.System[0].Text != "instructions" || len(request.Messages) != 1 || len(history) != 0 || len(newMessages) != 1 || len(request.Tools) != 1 {
		t.Fatalf("request=%+v history=%d new=%d", request, len(history), len(newMessages))
	}

	var encoded wireMessage
	if err := json.Unmarshal(request.Messages[0], &encoded); err != nil {
		t.Fatal(err)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(encoded.Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if encoded.Role != "user" || len(blocks) != 3 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "call_1" || !blocks[0].IsError || blocks[1].Text != "look" || blocks[2].Source == nil || blocks[2].Source.MediaType != "image/png" {
		t.Fatalf("message = %+v", blocks)
	}
}

func TestConfigureRequestValidatesThinkingBudget(t *testing.T) {
	client := &Client{requestOptions: func(agent.Request) (RequestOptions, error) {
		return RequestOptions{MaxTokens: 100, Thinking: &Thinking{Type: "enabled", BudgetTokens: 100}}, nil
	}}
	if err := client.configureRequest(agent.Request{}, &createRequest{}); err == nil {
		t.Fatal("invalid thinking budget was accepted")
	}
}

func TestShouldCompactOversizedState(t *testing.T) {
	content, _ := json.Marshal([]contentBlock{{Type: "text", Text: strings.Repeat("x", 200)}})
	large, _ := json.Marshal(wireMessage{Role: "assistant", Content: content})
	state, err := encodeState(nil, nil, []json.RawMessage{large}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{maxStateBytes: 128, stateOutputHeadroom: 32}
	if !client.ShouldCompactState(agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput("next")}}) {
		t.Fatal("oversized state did not trigger compaction")
	}
}

func TestContinuationStateRejectsWrongVersion(t *testing.T) {
	if _, err := decodeState([]byte(`{"version":2,"messages":[]}`), defaultMaxStateBytes); err == nil {
		t.Fatal("wrong state version was accepted")
	}
}
