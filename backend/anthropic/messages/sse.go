package messages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type streamEvent struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index,omitempty"`
	Message      *streamMessage        `json:"message,omitempty"`
	ContentBlock json.RawMessage       `json:"content_block,omitempty"`
	Delta        streamDelta           `json:"delta,omitempty"`
	Usage        *wireUsage            `json:"usage,omitempty"`
	Error        *backendhttp.APIError `json:"error,omitempty"`
}

type streamMessage struct {
	StopReason string     `json:"stop_reason,omitempty"`
	Usage      *wireUsage `json:"usage,omitempty"`
}

type streamDelta struct {
	Type        string     `json:"type,omitempty"`
	Text        string     `json:"text,omitempty"`
	Thinking    string     `json:"thinking,omitempty"`
	Signature   string     `json:"signature,omitempty"`
	PartialJSON string     `json:"partial_json,omitempty"`
	StopReason  string     `json:"stop_reason,omitempty"`
	Usage       *wireUsage `json:"usage,omitempty"`
}

type wireUsage struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
}

type usageAccumulator struct {
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64
}

type streamResult struct {
	text      string
	calls     []agent.ToolCall
	assistant json.RawMessage
	usage     agent.Usage
}

type streamDecoder struct {
	observer   backendhttp.Observer
	blocks     map[int]*blockAccumulator
	usage      usageAccumulator
	stopReason string
	stopped    bool
}

func readMessagesSSE(reader io.Reader, observer agent.StreamObserver) (streamResult, error) {
	decoder := streamDecoder{observer: backendhttp.NewObserver(observer), blocks: make(map[int]*blockAccumulator)}
	done, err := backendhttp.ReadSSE(reader, decoder.handleData)
	if err != nil {
		return streamResult{}, fmt.Errorf("read Anthropic Messages SSE: %w", err)
	}
	if !done {
		return streamResult{}, backendhttp.ErrIncompleteStream
	}
	return decoder.finish()
}

func (decoder *streamDecoder) handleData(data []byte) (bool, error) {
	if bytes.Equal(data, []byte("[DONE]")) {
		return false, nil
	}

	var event streamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("decode Anthropic Messages SSE event: %w", err)
	}
	if event.Type == "error" {
		detail := backendhttp.APIError{}
		if event.Error != nil {
			detail = *event.Error
		}
		return false, backendhttp.NewFailureError("anthropic messages SSE error", detail)
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

	block, update, err := newBlockAccumulator(index, raw)
	if err != nil {
		return err
	}
	decoder.blocks[index] = block
	return decoder.deliverBlockUpdate(block, update, false)
}

func (decoder *streamDecoder) updateBlock(index int, delta streamDelta) error {
	block, exists := decoder.blocks[index]
	if !exists {
		return fmt.Errorf("anthropic content block %d received a delta before start", index)
	}

	update, err := block.apply(delta)
	if err != nil {
		return err
	}
	return decoder.deliverBlockUpdate(block, update, false)
}

func (decoder *streamDecoder) stopBlock(index int) error {
	block, exists := decoder.blocks[index]
	if !exists {
		return fmt.Errorf("anthropic content block %d stopped before start", index)
	}

	update, err := block.stop()
	if err != nil {
		return err
	}
	return decoder.deliverBlockUpdate(block, update, true)
}

func (decoder *streamDecoder) deliverBlockUpdate(block *blockAccumulator, update blockUpdate, complete bool) error {
	if err := decoder.observer.Text(update.text); err != nil {
		return err
	}
	if err := decoder.observer.Reasoning(update.reasoning); err != nil {
		return err
	}
	if update.tool {
		return decoder.observer.ToolCall(block.toolID, block.toolName, block.toolArguments(), complete)
	}
	return nil
}

func (decoder *streamDecoder) finish() (streamResult, error) {
	if !decoder.stopped || decoder.stopReason == "" {
		return streamResult{}, backendhttp.ErrIncompleteStream
	}
	switch decoder.stopReason {
	case "end_turn", "stop_sequence", "tool_use", "refusal":
	case "model_context_window_exceeded":
		return streamResult{}, backendhttp.ErrContextLimit
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
	for _, index := range indexes {
		wire, call, blockText, err := decoder.blocks[index].finalize()
		if err != nil {
			return streamResult{}, err
		}
		wireBlocks = append(wireBlocks, wire)
		text.WriteString(blockText)
		if call != nil {
			calls = append(calls, *call)
		}
	}

	assistant, err := marshalWireMessage("assistant", wireBlocks)
	if err != nil {
		return streamResult{}, err
	}
	return streamResult{text: text.String(), calls: calls, assistant: assistant, usage: decoder.normalizeUsage()}, nil
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

func (decoder *streamDecoder) normalizeUsage() agent.Usage {
	usage := decoder.usage
	input := usage.inputTokens + usage.cacheReadTokens + usage.cacheCreationTokens
	return agent.Usage{InputTokens: input, OutputTokens: usage.outputTokens, TotalTokens: input + usage.outputTokens}
}
