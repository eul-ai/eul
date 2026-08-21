package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

func TestBuildCreateRequest(t *testing.T) {
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)})
	if err != nil {
		t.Fatal(err)
	}

	build, err := buildGenerationWireRequest(agent.Request{
		Model:    "model",
		FastMode: true,
		State:    state,
		Inputs:   []agent.Input{{Kind: agent.InputToolResult, CallID: "call_1", Tool: "read", Text: "failed", IsError: true}},
		Tools:    []agent.ToolDefinition{strictTestTool("read")},
	}, continuation.DefaultMaximumBytes, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}

	if build.wire.Model != "model" || build.wire.ServiceTier != "" || len(build.wire.Input) != 2 || len(build.newItems) != 1 || len(build.wire.Tools) != 1 || !build.wire.Tools[0].Strict {
		t.Fatalf("build=%+v", build)
	}
	compact, err := buildCompactRequest(agent.Request{Model: "model", FastMode: true}, continuation.DefaultMaximumBytes)
	if err != nil || compact.wire.ServiceTier != "" {
		t.Fatalf("compact request=%+v error=%v", compact, err)
	}
	if !strings.Contains(string(build.newItems[0]), `[tool error]\nfailed`) {
		t.Fatalf("tool result = %s", build.newItems[0])
	}
}

func TestEncodeInboxInputAsAgentMessage(t *testing.T) {
	items, err := encodeInputsWithOptions([]agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}}, true)
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
	items, err := encodeInputs([]agent.Input{agent.NewInboxInput("result")})
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
	build, err := buildGenerationWireRequestWithOptions(agent.Request{Inputs: []agent.Input{{Kind: agent.InputInbox, Text: "<subagent_notifications>result</subagent_notifications>"}}}, continuation.DefaultMaximumBytes, continuation.DefaultMaximumBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, build.newItems)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := continuation.Decode(state, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.wire.Input) != 1 || len(restored) != 1 || string(build.wire.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", build.wire.Input, restored)
	}
}

func TestBuildCreateRequestRejectsInvalidInputKinds(t *testing.T) {
	for _, input := range []agent.Input{
		{Kind: "unknown", Text: "value"},
		{Kind: agent.InputInbox, Content: []agent.ContentPart{{Kind: agent.ContentPartText, Text: "invalid"}}},
	} {
		if _, err := buildGenerationWireRequest(agent.Request{Inputs: []agent.Input{input}}, continuation.DefaultMaximumBytes, continuation.DefaultMaximumBytes); err == nil {
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
	build, err := buildGenerationWireRequest(agent.Request{Inputs: []agent.Input{{Kind: agent.InputUser, Content: parts}}}, continuation.DefaultMaximumBytes, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	state, err := continuation.Encode(continuation.DefaultMaximumBytes, build.newItems)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := continuation.Decode(state, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.wire.Input) != 1 || len(restored) != 1 || string(build.wire.Input[0]) != string(restored[0]) {
		t.Fatalf("request = %s, restored = %s", build.wire.Input, restored)
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
	build, err := buildCompactRequest(agent.Request{
		Model:  "model",
		Inputs: []agent.Input{agent.NewTextInput("hello")},
	}, continuation.DefaultMaximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.wire.Input) != 2 || len(build.input) != 1 {
		t.Fatalf("compact input count = %d, want 2", len(build.wire.Input))
	}

	var trigger struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(build.wire.Input[1], &trigger); err != nil || trigger.Type != "compaction_trigger" {
		t.Fatalf("compact trigger = %s, error = %v", build.wire.Input[1], err)
	}
}

func TestCompactedStateItemsRetainsOnlyGenuineUserMessages(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning"}`),
		json.RawMessage(`{"type":"message","role":"assistant"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"human"}]}`),
		json.RawMessage(`{"role":"user","content":"legacy inbox"}`),
		json.RawMessage(`{"type":"agent_message","content":[{"type":"input_text","text":"inbox"}]}`),
		json.RawMessage(`{"role":"developer","content":[{"type":"input_text","text":"old instructions"}]}`),
		json.RawMessage(`{"role":"system","content":[{"type":"input_text","text":"old system"}]}`),
		json.RawMessage(`{"type":"compaction","encrypted_content":"old"}`),
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"new"}`)}

	items := compactedStateItems(input, output)
	if len(items) != 2 || !strings.Contains(string(items[0]), `"text":"human"`) || !strings.Contains(string(items[1]), `"encrypted_content":"new"`) {
		t.Fatalf("compacted items = %s", items)
	}
}

func TestCompactedStateItemsKeepsNewestUsersWithinBudget(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"oldest message"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"ééé"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"new"}]}`),
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction"}`)}

	items := compactedStateItemsWithBudget(input, output, 2)
	if len(items) != 3 || !strings.Contains(string(items[0]), `"text":"éé"`) || !strings.Contains(string(items[1]), `"text":"new"`) || string(items[2]) != `{"type":"compaction"}` {
		t.Fatalf("compacted items = %s", items)
	}
}

func TestCompactedStateItemsAccountsForImages(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,abc"}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"new"}]}`),
	}

	withoutImage := compactedStateItemsWithBudget(input, nil, retainedImageTokens)
	if len(withoutImage) != 1 || !strings.Contains(string(withoutImage[0]), `"text":"new"`) {
		t.Fatalf("without image = %s", withoutImage)
	}

	withImage := compactedStateItemsWithBudget(input, nil, retainedImageTokens+1)
	if len(withImage) != 2 || !strings.Contains(string(withImage[0]), `"input_image"`) || !strings.Contains(string(withImage[1]), `"text":"new"`) {
		t.Fatalf("with image = %s", withImage)
	}
}

func TestBuildCreateRequestReservesResponseOutput(t *testing.T) {
	state, err := continuation.Encode(240, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + strings.Repeat("x", 40) + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput(strings.Repeat("y", 30))}}

	if _, err := buildGenerationWireRequest(request, 240, 240); err != nil {
		t.Fatalf("request did not fit full state limit: %v", err)
	}
	if _, err := buildGenerationWireRequest(request, 240, 100); err == nil {
		t.Fatalf("reserved request error = %v", err)
	}
	if _, err := buildCompactRequest(request, 240); err != nil {
		t.Fatalf("compact request could not decode full state: %v", err)
	}
}

func TestBuildCreateRequestRejectsInputsThatCannotFitState(t *testing.T) {
	_, err := buildGenerationWireRequest(agent.Request{
		Inputs: []agent.Input{{
			Kind: agent.InputUser,
			Content: []agent.ContentPart{{
				Kind:  agent.ContentPartImage,
				Image: &agent.Image{MediaType: "image/png", Data: make([]byte, 100)},
			}},
		}},
	}, 100, 100)
	if err == nil {
		t.Fatalf("error = %v", err)
	}
}

func TestConfiguredRequestOwnsMutableOptions(t *testing.T) {
	reasoning := &Reasoning{Effort: "high", Summary: "auto"}
	include := []string{"reasoning.encrypted_content"}
	configured := configureCommonRequest(createResponseRequest{}, RequestOptions{
		Reasoning: reasoning,
		Include:   include,
	})

	reasoning.Effort = "low"
	include[0] = "changed"
	if configured.Reasoning == reasoning || configured.Reasoning.Effort != "high" || configured.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("configured request retained mutable options: %+v", configured)
	}
}
