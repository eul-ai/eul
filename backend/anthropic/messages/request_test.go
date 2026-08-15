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

func TestPromptCacheControlMarksStableBoundariesWithoutMutatingRequest(t *testing.T) {
	message := func(role string, blocks []contentBlock) json.RawMessage {
		content, _ := json.Marshal(blocks)
		raw, _ := json.Marshal(wireMessage{Role: role, Content: content})
		return raw
	}
	request := createRequest{
		System: []systemBlock{{Type: "text", Text: "first"}, {Type: "text", Text: "last"}},
		Tools:  []toolDefinition{{Name: "first"}, {Name: "last"}},
		Messages: []json.RawMessage{
			message("user", []contentBlock{{Type: "text", Text: "old"}}),
			message("assistant", []contentBlock{{Type: "thinking", Thinking: "thought", Signature: "signed"}, {Type: "tool_use", ID: "call", Name: "read", Input: json.RawMessage(`{}`)}}),
			message("user", []contentBlock{{Type: "text", Text: "latest"}, {Type: "image", Source: &imageSource{Type: "base64", MediaType: "image/png", Data: "cG5n"}}}),
		},
	}

	cached, err := withPromptCacheControl(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(cached)
	if strings.Count(string(encoded), `"cache_control"`) != 4 {
		t.Fatalf("cached request = %s", encoded)
	}
	if cached.Tools[0].CacheControl != nil || cached.Tools[1].CacheControl == nil || cached.System[0].CacheControl != nil || cached.System[1].CacheControl == nil {
		t.Fatalf("tools=%+v system=%+v", cached.Tools, cached.System)
	}

	var latest wireMessage
	if err := json.Unmarshal(cached.Messages[2], &latest); err != nil {
		t.Fatal(err)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(latest.Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if blocks[0].CacheControl != nil || blocks[1].CacheControl == nil {
		t.Fatalf("latest user blocks = %+v", blocks)
	}
	if strings.Contains(string(cached.Messages[0]), `"cache_control"`) || !strings.Contains(string(cached.Messages[1]), `"cache_control"`) {
		t.Fatalf("message cache controls are misplaced: %s", cached.Messages)
	}

	original, _ := json.Marshal(request)
	if strings.Contains(string(original), `"cache_control"`) {
		t.Fatalf("original request was mutated: %s", original)
	}
}

func TestConfigureRequestValidatesThinkingBudget(t *testing.T) {
	for _, test := range []struct {
		name      string
		maxTokens int
		budget    int
		wantError bool
	}{
		{name: "below minimum", maxTokens: 2000, budget: 1023, wantError: true},
		{name: "not below max", maxTokens: 1024, budget: 1024, wantError: true},
		{name: "valid", maxTokens: 1025, budget: 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{requestOptions: func(agent.Request) (RequestOptions, error) {
				return RequestOptions{MaxTokens: test.maxTokens, Thinking: &Thinking{Type: "enabled", BudgetTokens: test.budget}}, nil
			}}
			err := client.configureRequest(agent.Request{}, &createRequest{})
			if (err != nil) != test.wantError {
				t.Fatalf("configureRequest() error = %v", err)
			}
		})
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
