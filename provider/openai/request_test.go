package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"yaah/agent"
)

func TestBuildCreateRequest(t *testing.T) {
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	request, newItems, err := buildCreateRequest(agent.Request{
		Model:  "model",
		State:  state,
		Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Text: "failed", IsError: true}},
		Tools:  []agent.ToolDefinition{strictTestTool("read")},
	}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	if request.Model != "model" || len(request.Input) != 2 || len(newItems) != 1 || len(request.Tools) != 1 || request.Tools[0].Strict != nil {
		t.Fatalf("request=%+v newItems=%s", request, newItems)
	}
	if !strings.Contains(string(newItems[0]), `[tool error]\nfailed`) {
		t.Fatalf("tool result = %s", newItems[0])
	}
}

func TestContinuationStateVersionAndBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		state []byte
		max   int
		want  string
	}{
		{name: "malformed", state: []byte(`{`), max: 100, want: "decode continuation state"},
		{name: "version", state: []byte(`{"version":2,"items":[]}`), max: 100, want: "unsupported continuation state version"},
		{name: "oversized", state: []byte(strings.Repeat("x", 11)), max: 10, want: "exceeds 10 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeState(test.state, test.max); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	if _, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"large":"` + strings.Repeat("x", 100) + `"}`)}, 50); err == nil || !strings.Contains(err.Error(), "exceeds 50 bytes") {
		t.Fatalf("oversized encoded state error = %v", err)
	}
}
