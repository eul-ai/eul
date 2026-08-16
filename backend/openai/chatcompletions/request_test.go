package chatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

func TestBuildRequestEncodesInputsToolsAndState(t *testing.T) {
	stateMessage := json.RawMessage(`{"role":"assistant","content":"earlier"}`)
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, []json.RawMessage{stateMessage})
	if err != nil {
		t.Fatal(err)
	}

	build, err := buildGenerationRequest(agent.Request{
		Model:        "model",
		Instructions: "instructions",
		State:        state,
		Inputs: []agent.Input{
			agent.NewUserInput(
				agent.ContentPart{Kind: agent.ContentPartText, Text: "look "},
				agent.ContentPart{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
				agent.ContentPart{Kind: agent.ContentPartText, Text: "here"},
			),
			agent.NewToolResultInput(agent.ToolResult{CallID: "call_1", Tool: "read", Output: "failed", IsError: true}),
		},
		Tools: []agent.ToolDefinition{{
			Name:        "read",
			Description: "Read a file",
			Parameters:  agent.JSONSchema{Type: "object"},
		}},
	}, continuation.DefaultMaximumBytes, continuation.GenerationStateBytes(continuation.DefaultMaximumBytes, 0))
	if err != nil {
		t.Fatal(err)
	}
	request := build.wire
	if request.Model != "model" || len(request.Messages) != 4 || len(build.history) != 1 || len(build.newMessages) != 2 || len(request.Tools) != 1 || !request.Tools[0].Function.Strict {
		t.Fatalf("request = %+v, history=%d new=%d", request, len(build.history), len(build.newMessages))
	}

	var system message
	if err := json.Unmarshal(request.Messages[0], &system); err != nil || system.Role != "system" || system.Content != "instructions" {
		t.Fatalf("system message = %s, error = %v", request.Messages[0], err)
	}
	if string(request.Messages[1]) != string(stateMessage) {
		t.Fatalf("state message = %s", request.Messages[1])
	}
	var user struct {
		Role    string        `json:"role"`
		Content []contentPart `json:"content"`
	}
	if err := json.Unmarshal(request.Messages[2], &user); err != nil {
		t.Fatal(err)
	}
	if user.Role != "user" || len(user.Content) != 3 || user.Content[0].Text != "look " || user.Content[2].Text != "here" || !strings.HasPrefix(user.Content[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("user message = %+v", user)
	}
	var tool message
	if err := json.Unmarshal(request.Messages[3], &tool); err != nil || tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "[tool error]\nfailed" {
		t.Fatalf("tool message = %s, error = %v", request.Messages[3], err)
	}
}

func TestEncodeUserContentConcatenatesTextOnlyParts(t *testing.T) {
	content := encodeUserContent([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "one"},
		{Kind: agent.ContentPartText, Text: "two"},
	})
	if content != "onetwo" {
		t.Fatalf("content = %#v", content)
	}
}

func TestShouldCompactOversizedState(t *testing.T) {
	large, _ := json.Marshal(message{Role: "assistant", Content: strings.Repeat("x", 200)})
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, []json.RawMessage{large})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{maxStateBytes: 128, stateOutputHeadroom: 32}
	if !client.ShouldCompactState(agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput("next")}}) {
		t.Fatal("oversized state did not trigger compaction")
	}
}
