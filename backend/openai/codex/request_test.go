package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestBuildCreateRequest(t *testing.T) {
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	request, newItems, err := buildCreateRequest(agent.Request{
		Model:    "model",
		FastMode: true,
		State:    state,
		Inputs:   []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Text: "failed", IsError: true}},
		Tools:    []agent.ToolDefinition{strictTestTool("read")},
	}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	if request.Model != "model" || request.ServiceTier != "priority" || len(request.Input) != 2 || len(newItems) != 1 || len(request.Tools) != 1 || request.Tools[0].Strict != nil {
		t.Fatalf("request=%+v newItems=%s", request, newItems)
	}
	compact, err := buildCompactRequest(agent.Request{Model: "model", FastMode: true}, defaultMaxStateBytes)
	if err != nil || compact.ServiceTier != "priority" {
		t.Fatalf("compact request=%+v error=%v", compact, err)
	}
	if !strings.Contains(string(newItems[0]), `[tool error]\nfailed`) {
		t.Fatalf("tool result = %s", newItems[0])
	}
}

func TestEncodeInputImages(t *testing.T) {
	items := encodeInputs([]agent.Input{{
		Kind: agent.InputUser,
		Text: "describe this",
		Images: &agent.ImageAttachments{Items: []agent.Image{{
			MediaType: "image/png",
			Data:      []byte("png"),
		}}},
	}})
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}

	encoded := string(items[0])
	if !strings.Contains(encoded, `"type":"input_text","text":"describe this"`) ||
		!strings.Contains(encoded, `"type":"input_image","image_url":"data:image/png;base64,cG5n"`) {
		t.Fatalf("input = %s", encoded)
	}
}

func TestBuildCompactRequest(t *testing.T) {
	request, err := buildCompactRequest(agent.Request{
		Model:  "model",
		Inputs: []agent.Input{{Kind: agent.InputUser, Text: "hello"}},
	}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 2 {
		t.Fatalf("compact input count = %d, want 2", len(request.Input))
	}

	var trigger struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(request.Input[1], &trigger); err != nil || trigger.Type != "compaction_trigger" {
		t.Fatalf("compact trigger = %s, error = %v", request.Input[1], err)
	}
}

func TestCompactedStateItems(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning"}`),
		json.RawMessage(`{"type":"message","role":"assistant"}`),
		json.RawMessage(`{"type":"message","role":"user"}`),
		json.RawMessage(`{"type":"agent_message"}`),
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction"}`)}

	items := compactedStateItems(input, output)
	if len(items) != 3 || !strings.Contains(string(items[0]), `"role":"user"`) || !strings.Contains(string(items[1]), `"type":"agent_message"`) || !strings.Contains(string(items[2]), `"type":"compaction"`) {
		t.Fatalf("compacted items = %s", items)
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
