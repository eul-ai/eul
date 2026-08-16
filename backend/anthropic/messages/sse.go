package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

var errSSEIncomplete = errors.New("anthropic messages SSE stream ended without message_stop")

type contextWindowExceededError struct{}

func (contextWindowExceededError) Error() string {
	return "anthropic message exceeded the model context window"
}

type observerDeliveryError struct {
	operation string
	cause     error
}

func (err *observerDeliveryError) Error() string { return err.operation + ": " + err.cause.Error() }
func (err *observerDeliveryError) Unwrap() error { return err.cause }

type partialResponseError struct {
	cause error
}

func (err *partialResponseError) Error() string { return err.cause.Error() }
func (err *partialResponseError) Unwrap() error { return err.cause }

type streamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Message      *streamMessage  `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        streamDelta     `json:"delta"`
	Usage        *wireUsage      `json:"usage"`
	Error        *apiError       `json:"error"`
}

type streamMessage struct {
	StopReason string     `json:"stop_reason"`
	Usage      *wireUsage `json:"usage"`
}

type streamDelta struct {
	Type        string     `json:"type"`
	Text        string     `json:"text"`
	Thinking    string     `json:"thinking"`
	Signature   string     `json:"signature"`
	PartialJSON string     `json:"partial_json"`
	StopReason  string     `json:"stop_reason"`
	Usage       *wireUsage `json:"usage"`
}

type wireUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

type usageAccumulator struct {
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64
}

type blockAccumulator struct {
	index        int
	kind         string
	text         strings.Builder
	thinking     strings.Builder
	signature    strings.Builder
	toolID       string
	toolName     string
	initialInput json.RawMessage
	toolInput    strings.Builder
	hasToolDelta bool
	redacted     string
	complete     bool
}

type streamResult struct {
	text      string
	calls     []agent.ToolCall
	assistant json.RawMessage
	usage     agent.Usage
}

type streamDecoder struct {
	observer   agent.StreamObserver
	blocks     map[int]*blockAccumulator
	usage      usageAccumulator
	stopReason string
	stopped    bool
	observed   bool
}

func readMessagesSSE(reader io.Reader, maximum int64, observer agent.StreamObserver) (streamResult, error) {
	decoder := streamDecoder{observer: observer, blocks: make(map[int]*blockAccumulator)}
	done, err := backendhttp.ReadSSE(reader, maximum, decoder.handleData)
	if err != nil {
		return streamResult{}, decoder.wrapPartial(fmt.Errorf("read Anthropic Messages SSE: %w", err))
	}
	if !done {
		return streamResult{}, decoder.wrapPartial(errSSEIncomplete)
	}
	result, err := decoder.finish()
	return result, decoder.wrapPartial(err)
}

func (decoder *streamDecoder) handleData(data []byte) (bool, error) {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return false, nil
	}

	var event streamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("decode Anthropic Messages SSE event: %w", err)
	}
	if event.Type == "error" {
		detail := apiError{}
		if event.Error != nil {
			detail = *event.Error
		}
		return false, &responseFailureError{message: "anthropic messages SSE error: " + formatAPIError(detail), detail: detail}
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			decoder.mergeUsage(event.Message.Usage)
			if event.Message.StopReason != "" {
				decoder.stopReason = event.Message.StopReason
			}
		}
	case "content_block_start":
		return false, decoder.startBlock(event.Index, event.ContentBlock)
	case "content_block_delta":
		return false, decoder.updateBlock(event.Index, event.Delta)
	case "content_block_stop":
		return false, decoder.stopBlock(event.Index)
	case "message_delta":
		if event.Delta.StopReason != "" {
			decoder.stopReason = event.Delta.StopReason
		}
		decoder.mergeUsage(event.Usage)
		decoder.mergeUsage(event.Delta.Usage)
	case "message_stop":
		decoder.stopped = true
		return true, nil
	case "ping":
	}
	return false, nil
}

func (decoder *streamDecoder) startBlock(index int, raw json.RawMessage) error {
	if _, exists := decoder.blocks[index]; exists {
		return fmt.Errorf("anthropic content block %d started more than once", index)
	}
	if err := validateRawObject(raw); err != nil {
		return fmt.Errorf("anthropic content block %d: %w", index, err)
	}

	var block contentBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return fmt.Errorf("decode Anthropic content block %d: %w", index, err)
	}
	accumulator := &blockAccumulator{
		index:        index,
		kind:         block.Type,
		toolID:       block.ID,
		toolName:     block.Name,
		initialInput: append(json.RawMessage(nil), block.Input...),
		redacted:     block.Data,
	}
	accumulator.text.WriteString(block.Text)
	accumulator.thinking.WriteString(block.Thinking)
	accumulator.signature.WriteString(block.Signature)
	decoder.blocks[index] = accumulator

	switch block.Type {
	case "text":
		return decoder.deliverText(block.Text)
	case "thinking":
		return decoder.deliverReasoning(block.Thinking)
	case "tool_use":
		if block.ID == "" || block.Name == "" {
			return fmt.Errorf("anthropic tool block %d starts without an ID and name", index)
		}
		return decoder.deliverToolCall(accumulator, false)
	case "redacted_thinking":
		return nil
	default:
		return fmt.Errorf("unsupported Anthropic content block type %q", block.Type)
	}
}

func (decoder *streamDecoder) updateBlock(index int, delta streamDelta) error {
	block, exists := decoder.blocks[index]
	if !exists {
		return fmt.Errorf("anthropic content block %d received a delta before start", index)
	}
	if block.complete {
		return fmt.Errorf("anthropic content block %d received a delta after stop", index)
	}

	switch delta.Type {
	case "text_delta":
		if block.kind != "text" {
			return fmt.Errorf("anthropic content block %d received text for type %q", index, block.kind)
		}
		block.text.WriteString(delta.Text)
		return decoder.deliverText(delta.Text)
	case "thinking_delta":
		if block.kind != "thinking" {
			return fmt.Errorf("anthropic content block %d received thinking for type %q", index, block.kind)
		}
		block.thinking.WriteString(delta.Thinking)
		return decoder.deliverReasoning(delta.Thinking)
	case "signature_delta":
		if block.kind != "thinking" {
			return fmt.Errorf("anthropic content block %d received a signature for type %q", index, block.kind)
		}
		block.signature.WriteString(delta.Signature)
	case "input_json_delta":
		if block.kind != "tool_use" {
			return fmt.Errorf("anthropic content block %d received tool input for type %q", index, block.kind)
		}
		block.hasToolDelta = true
		block.toolInput.WriteString(delta.PartialJSON)
		return decoder.deliverToolCall(block, false)
	default:
		return fmt.Errorf("unsupported Anthropic content block delta type %q", delta.Type)
	}
	return nil
}

func (decoder *streamDecoder) stopBlock(index int) error {
	block, exists := decoder.blocks[index]
	if !exists {
		return fmt.Errorf("anthropic content block %d stopped before start", index)
	}
	if block.complete {
		return fmt.Errorf("anthropic content block %d stopped more than once", index)
	}
	block.complete = true
	if block.kind == "tool_use" {
		arguments := block.toolArguments()
		if err := validateRawObject(json.RawMessage(arguments)); err != nil {
			return fmt.Errorf("anthropic tool call %q input: %w", block.toolID, err)
		}
		return decoder.deliverToolCall(block, true)
	}
	return nil
}

func (decoder *streamDecoder) deliverText(delta string) error {
	if delta == "" || decoder.observer.Text == nil {
		return nil
	}
	if err := decoder.observer.Text(delta); err != nil {
		return &observerDeliveryError{operation: "deliver text", cause: err}
	}
	decoder.observed = true
	return nil
}

func (decoder *streamDecoder) deliverReasoning(delta string) error {
	if delta == "" || decoder.observer.Reasoning == nil {
		return nil
	}
	if err := decoder.observer.Reasoning(delta); err != nil {
		return &observerDeliveryError{operation: "deliver reasoning", cause: err}
	}
	decoder.observed = true
	return nil
}

func (decoder *streamDecoder) deliverToolCall(block *blockAccumulator, complete bool) error {
	if decoder.observer.ToolCall == nil {
		return nil
	}
	if err := decoder.observer.ToolCall(agent.ToolCallSnapshot{
		ID:           block.toolID,
		Name:         block.toolName,
		RawArguments: block.toolArguments(),
		Complete:     complete,
	}); err != nil {
		return &observerDeliveryError{operation: "deliver tool call", cause: err}
	}
	decoder.observed = true
	return nil
}

func (block *blockAccumulator) toolArguments() string {
	if block.hasToolDelta {
		arguments := block.toolInput.String()
		if arguments != "" {
			return arguments
		}
	}
	if len(block.initialInput) != 0 && string(block.initialInput) != "null" {
		return string(block.initialInput)
	}
	return "{}"
}

func (decoder *streamDecoder) finish() (streamResult, error) {
	if !decoder.stopped || decoder.stopReason == "" {
		return streamResult{}, errSSEIncomplete
	}
	switch decoder.stopReason {
	case "end_turn", "stop_sequence", "tool_use", "refusal":
	case "model_context_window_exceeded":
		return streamResult{}, contextWindowExceededError{}
	case "max_tokens", "pause_turn":
		return streamResult{}, fmt.Errorf("anthropic message stopped with reason %q", decoder.stopReason)
	default:
		return streamResult{}, fmt.Errorf("anthropic message has unsupported stop reason %q", decoder.stopReason)
	}

	indexes := make([]int, 0, len(decoder.blocks))
	for index := range decoder.blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	var text strings.Builder
	calls := make([]agent.ToolCall, 0)
	wireBlocks := make([]contentBlock, 0, len(indexes))
	seenCalls := make(map[string]struct{})
	for _, index := range indexes {
		block := decoder.blocks[index]
		if !block.complete {
			return streamResult{}, fmt.Errorf("anthropic content block %d did not stop", index)
		}

		var value contentBlock
		switch block.kind {
		case "text":
			value = contentBlock{Type: "text", Text: block.text.String()}
			text.WriteString(value.Text)
		case "thinking":
			value = contentBlock{Type: "thinking", Thinking: block.thinking.String(), Signature: block.signature.String()}
		case "redacted_thinking":
			value = contentBlock{Type: "redacted_thinking", Data: block.redacted}
		case "tool_use":
			arguments := block.toolArguments()
			rawInput := json.RawMessage(arguments)
			if err := validateRawObject(rawInput); err != nil {
				return streamResult{}, fmt.Errorf("anthropic tool call %q input: %w", block.toolID, err)
			}
			if _, exists := seenCalls[block.toolID]; exists {
				return streamResult{}, fmt.Errorf("anthropic message has duplicate tool call ID %q", block.toolID)
			}
			seenCalls[block.toolID] = struct{}{}
			calls = append(calls, agent.ToolCall{ID: block.toolID, Name: block.toolName, Arguments: rawInput})
			value = contentBlock{Type: "tool_use", ID: block.toolID, Name: block.toolName, Input: rawInput}
		}
		wireBlocks = append(wireBlocks, value)
	}

	assistant, err := marshalWireMessage("assistant", wireBlocks)
	if err != nil {
		return streamResult{}, err
	}
	usage, err := decoder.normalizeUsage()
	if err != nil {
		return streamResult{}, err
	}
	return streamResult{text: text.String(), calls: calls, assistant: assistant, usage: usage}, nil
}

func (decoder *streamDecoder) mergeUsage(usage *wireUsage) {
	if usage == nil {
		return
	}
	if usage.InputTokens != nil {
		decoder.usage.inputTokens = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		decoder.usage.outputTokens = *usage.OutputTokens
	}
	if usage.CacheReadInputTokens != nil {
		decoder.usage.cacheReadTokens = *usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		decoder.usage.cacheCreationTokens = *usage.CacheCreationInputTokens
	}
}

func (decoder *streamDecoder) normalizeUsage() (agent.Usage, error) {
	usage := decoder.usage
	if usage.inputTokens < 0 || usage.outputTokens < 0 || usage.cacheReadTokens < 0 || usage.cacheCreationTokens < 0 {
		return agent.Usage{}, errors.New("anthropic message usage contains negative token counts")
	}
	input := usage.inputTokens + usage.cacheReadTokens + usage.cacheCreationTokens
	return agent.Usage{InputTokens: input, OutputTokens: usage.outputTokens, TotalTokens: input + usage.outputTokens}, nil
}

func (decoder *streamDecoder) wrapPartial(err error) error {
	if err == nil || !decoder.observed {
		return err
	}
	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return err
	}
	return &partialResponseError{cause: err}
}
