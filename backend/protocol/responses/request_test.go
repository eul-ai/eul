package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
)

func TestBuildCreateRequest(t *testing.T) {
	history := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}
	wire, newItems, err := testCodec().build(agent.Request{
		Model:  "model",
		Inputs: []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read", Text: "failed", IsError: true}},
		Tools:  []agent.ToolDefinition{strictTestTool("read")},
	}, history)
	if err != nil {
		t.Fatal(err)
	}

	if wire.Model != "model" || len(wire.Input) != 2 || len(newItems) != 1 || len(wire.Tools) != 1 || !wire.Tools[0].Strict || wire.ToolChoice != "auto" || !wire.ParallelToolCalls {
		t.Fatalf("wire=%+v newItems=%s", wire, newItems)
	}
	if !strings.Contains(string(newItems[0]), `[tool error]\nfailed`) {
		t.Fatalf("tool result = %s", newItems[0])
	}

	summary, _, err := testCodec().build(agent.Request{Model: "model"}, nil)
	if err != nil || summary.ToolChoice != "" || summary.ParallelToolCalls {
		t.Fatalf("request without tools = %+v, error = %v", summary, err)
	}
}

func TestEncodeInboxInputAsAgentMessage(t *testing.T) {
	items, err := Codec{InboxAsAgentMessage: true}.encodeInputs([]agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	var message inputAgentMessage
	if err := json.Unmarshal(items[0], &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "agent_message" || message.Author != "/root/subagents" || message.Recipient != "/root" || len(message.Content) != 1 || message.Content[0].Type != "input_text" || message.Content[0].Text != "<subagent_notifications>result</subagent_notifications>" {
		t.Fatalf("message = %+v", message)
	}
}

func TestEncodeInboxInputAsUserMessageByDefault(t *testing.T) {
	items, err := Codec{}.encodeInputs([]agent.Input{agent.NewInboxInput("result")})
	if err != nil {
		t.Fatal(err)
	}
	var message inputMessage
	if err := json.Unmarshal(items[0], &message); err != nil {
		t.Fatal(err)
	}
	if message.Role != "user" || message.Content != "result" {
		t.Fatalf("message = %+v", message)
	}
}

func TestInboxInputSurvivesContinuationState(t *testing.T) {
	codec := testCodec()
	codec.InboxAsAgentMessage = true
	wire, newItems, err := codec.build(agent.Request{Inputs: []agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := protocol.EncodeState(newItems)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := protocol.DecodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Input) != 1 || len(restored) != 1 || string(wire.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", wire.Input, restored)
	}
}

func TestBuildCreateRequestRejectsInvalidInputKinds(t *testing.T) {
	for _, input := range []agent.Input{
		{Kind: "unknown", Text: "value"},
	} {
		if _, _, err := testCodec().build(agent.Request{Inputs: []agent.Input{input}}, nil); err == nil {
			t.Fatalf("input %+v was accepted", input)
		}
	}
}

func TestEncodeInputContentInOrder(t *testing.T) {
	items, err := Codec{}.encodeInputs([]agent.Input{{
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
	wire, newItems, err := testCodec().build(agent.Request{Inputs: []agent.Input{{Kind: agent.InputUser, Content: parts}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := protocol.EncodeState(newItems)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := protocol.DecodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Input) != 1 || len(restored) != 1 || string(wire.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", wire.Input, restored)
	}
}

func TestEncodeTextAndImageOnlyContent(t *testing.T) {
	text, err := Codec{}.encodeInputs([]agent.Input{agent.NewTextInput("hello")})
	if err != nil {
		t.Fatal(err)
	}
	var textMessage struct {
		Content []inputContentPart `json:"content"`
	}
	if err := json.Unmarshal(text[0], &textMessage); err != nil || len(textMessage.Content) != 1 || textMessage.Content[0] != (inputContentPart{Type: "input_text", Text: "hello"}) {
		t.Fatalf("text input = %s, error = %v", text[0], err)
	}

	image, err := Codec{}.encodeInputs([]agent.Input{{
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
	built, newItems, err := testCodec().BuildCompaction(agent.Request{
		Model:  "model",
		Inputs: []agent.Input{agent.NewTextInput("hello")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire := built.(createResponseRequest)
	if len(wire.Input) != 2 || len(newItems) != 1 {
		t.Fatalf("compact input = %s, new items = %s", wire.Input, newItems)
	}

	var trigger struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(wire.Input[1], &trigger); err != nil || trigger.Type != "compaction_trigger" {
		t.Fatalf("compact trigger = %s, error = %v", wire.Input[1], err)
	}
}
