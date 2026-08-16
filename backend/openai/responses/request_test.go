package responses

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
		Inputs:   []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read", Text: "failed", IsError: true}},
		Tools:    []agent.ToolDefinition{strictTestTool("read")},
	}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}

	if request.Model != "model" || request.ServiceTier != "" || len(request.Input) != 2 || len(newItems) != 1 || len(request.Tools) != 1 || !request.Tools[0].Strict {
		t.Fatalf("request=%+v newItems=%s", request, newItems)
	}
	compact, err := buildCompactRequest(agent.Request{Model: "model", FastMode: true}, defaultMaxStateBytes)
	if err != nil || compact.ServiceTier != "" {
		t.Fatalf("compact request=%+v error=%v", compact, err)
	}
	if !strings.Contains(string(newItems[0]), `[tool error]\nfailed`) {
		t.Fatalf("tool result = %s", newItems[0])
	}
}

func TestEncodeInboxInputAsUserMessage(t *testing.T) {
	items, err := encodeInputs([]agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	var message inputMessage
	if err := json.Unmarshal(items[0], &message); err != nil {
		t.Fatal(err)
	}
	if message.Role != "user" || message.Content != "<subagent_notifications>result</subagent_notifications>" {
		t.Fatalf("message = %+v", message)
	}
}

func TestInboxInputSurvivesContinuationState(t *testing.T) {
	request, newItems, err := buildCreateRequest(agent.Request{Inputs: []agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}}}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	state, err := encodeState(nil, newItems, nil, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeState(state, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 1 || len(restored) != 1 || string(request.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", request.Input, restored)
	}
}

func TestBuildCreateRequestRejectsInvalidInputKinds(t *testing.T) {
	for _, input := range []agent.Input{
		{Kind: "unknown", Text: "value"},
		{Kind: agent.InputInbox, Content: []agent.ContentPart{{Kind: agent.ContentPartText, Text: "invalid"}}},
	} {
		if _, _, err := buildCreateRequest(agent.Request{Inputs: []agent.Input{input}}, defaultMaxStateBytes); err == nil {
			t.Fatalf("input %+v was accepted", input)
		}
	}
}

func TestEncodeInputContentInOrder(t *testing.T) {
	items, err := encodeInputs([]agent.Input{{
		Kind: agent.InputUser,
		Content: []agent.ContentPart{
			{Kind: agent.ContentPartText, Text: "before"},
			{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("one")}},
			{Kind: agent.ContentPartText, Text: "after"},
			{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("two")}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}

	var message struct {
		Content []inputContentPart `json:"content"`
	}
	if err := json.Unmarshal(items[0], &message); err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 4 ||
		message.Content[0] != (inputContentPart{Type: "input_text", Text: "before"}) ||
		message.Content[1] != (inputContentPart{Type: "input_image", ImageURL: "data:image/png;base64,b25l"}) ||
		message.Content[2] != (inputContentPart{Type: "input_text", Text: "after"}) ||
		message.Content[3] != (inputContentPart{Type: "input_image", ImageURL: "data:image/png;base64,dHdv"}) {
		t.Fatalf("content = %+v", message.Content)
	}
}

func TestOrderedInputContentSurvivesContinuationState(t *testing.T) {
	parts := []agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before"},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("one")}},
		{Kind: agent.ContentPartText, Text: "after"},
	}
	request, newItems, err := buildCreateRequest(agent.Request{Inputs: []agent.Input{{Kind: agent.InputUser, Content: parts}}}, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	state, err := encodeState(nil, newItems, nil, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeState(state, defaultMaxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 1 || len(restored) != 1 || string(request.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", request.Input, restored)
	}
}

func TestEncodeTextAndImageOnlyContent(t *testing.T) {
	text, err := encodeInputs([]agent.Input{agent.NewTextInput("hello")})
	if err != nil {
		t.Fatal(err)
	}
	var textMessage struct {
		Content []inputContentPart `json:"content"`
	}
	if err := json.Unmarshal(text[0], &textMessage); err != nil || len(textMessage.Content) != 1 || textMessage.Content[0] != (inputContentPart{Type: "input_text", Text: "hello"}) {
		t.Fatalf("text input = %s, error = %v", text[0], err)
	}

	image, err := encodeInputs([]agent.Input{{
		Kind: agent.InputUser,
		Content: []agent.ContentPart{{
			Kind:  agent.ContentPartImage,
			Image: &agent.Image{MediaType: "image/png", Data: []byte("png")},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var imageMessage struct {
		Content []inputContentPart `json:"content"`
	}
	if err := json.Unmarshal(image[0], &imageMessage); err != nil || len(imageMessage.Content) != 1 || imageMessage.Content[0].Type != "input_image" {
		t.Fatalf("image input = %s, error = %v", image[0], err)
	}
}

func TestBuildCompactRequest(t *testing.T) {
	request, err := buildCompactRequest(agent.Request{
		Model:  "model",
		Inputs: []agent.Input{agent.NewTextInput("hello")},
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

func TestBuildCreateRequestReservesResponseOutput(t *testing.T) {
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + strings.Repeat("x", 40) + `"}`)}, 240)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput(strings.Repeat("y", 30))}}

	if _, _, err := buildCreateRequest(request, 240); err != nil {
		t.Fatalf("request did not fit full state limit: %v", err)
	}
	if _, _, err := buildCreateRequestWithLimit(request, 240, 100); err == nil {
		t.Fatalf("reserved request error = %v", err)
	}
	if _, err := buildCompactRequest(request, 240); err != nil {
		t.Fatalf("compact request could not decode full state: %v", err)
	}
}

func TestBuildCreateRequestRejectsInputsThatCannotFitState(t *testing.T) {
	_, _, err := buildCreateRequest(agent.Request{
		Inputs: []agent.Input{{
			Kind: agent.InputUser,
			Content: []agent.ContentPart{{
				Kind:  agent.ContentPartImage,
				Image: &agent.Image{MediaType: "image/png", Data: make([]byte, 100)},
			}},
		}},
	}, 100)
	if err == nil {
		t.Fatalf("error = %v", err)
	}
}

func TestContinuationStateVersionAndBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		state []byte
		max   int
	}{
		{name: "malformed", state: []byte(`{`), max: 100},
		{name: "version", state: []byte(`{"version":2,"items":[]}`), max: 100},
		{name: "non-object item", state: []byte(`{"version":1,"items":[null]}`), max: 100},
		{name: "oversized", state: []byte(strings.Repeat("x", 11)), max: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeState(test.state, test.max); err == nil {
				t.Fatal("decodeState succeeded")
			}
		})
	}

	if _, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"large":"` + strings.Repeat("x", 100) + `"}`)}, 50); err == nil {
		t.Fatalf("oversized encoded state error = %v", err)
	}
}
