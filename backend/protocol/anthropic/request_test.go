package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestBuildRequestEncodesAnthropicInputs(t *testing.T) {
	codec := Codec{Options: func(agent.Request) (RequestOptions, error) { return RequestOptions{MaxTokens: 100}, nil }}
	built, newMessages, err := codec.Build(agent.Request{
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire := built.(createRequest)
	if wire.Model != "model" || len(wire.System) != 1 || wire.System[0].Text != "instructions" || len(wire.Messages) != 1 || len(newMessages) != 1 || len(wire.Tools) != 1 {
		t.Fatalf("request=%+v new=%d", wire, len(newMessages))
	}

	var encoded wireMessage
	if err := json.Unmarshal(newMessages[0], &encoded); err != nil {
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
