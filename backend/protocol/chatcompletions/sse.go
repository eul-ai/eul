package chatcompletions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/protocol"
)

type completionChunk struct {
	Choices []completionChoice    `json:"choices"`
	Usage   *completionUsage      `json:"usage"`
	Error   *backendhttp.APIError `json:"error"`
}

type completionChoice struct {
	Index        int             `json:"index"`
	Delta        completionDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type completionDelta struct {
	Content          string          `json:"content"`
	Refusal          string          `json:"refusal"`
	ReasoningContent string          `json:"reasoning_content"`
	Reasoning        string          `json:"reasoning"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type completionUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type streamDecoder struct {
	observer                  backendhttp.Observer
	text                      strings.Builder
	reasoning                 strings.Builder
	toolCalls                 toolCallAccumulator
	usage                     *completionUsage
	finishReason              string
	serializeReasoningContent bool
}

func readCompletionSSE(reader io.Reader, observer agent.StreamObserver, serializeReasoningContent bool) (protocol.Result, error) {
	decoder := streamDecoder{
		observer:                  backendhttp.NewObserver(observer),
		toolCalls:                 newToolCallAccumulator(),
		serializeReasoningContent: serializeReasoningContent,
	}
	done, err := backendhttp.ReadSSE(reader, decoder.handleData)
	if err != nil {
		return protocol.Result{}, fmt.Errorf("read Chat Completions SSE: %w", err)
	}
	if !done && decoder.finishReason == "" {
		return protocol.Result{}, backendhttp.ErrIncompleteStream
	}
	return decoder.finish()
}

func (decoder *streamDecoder) handleData(data []byte) (bool, error) {
	if bytes.Equal(data, []byte("[DONE]")) {
		return true, nil
	}

	var chunk completionChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false, fmt.Errorf("decode Chat Completions SSE event: %w", err)
	}
	if chunk.Error != nil {
		return false, backendhttp.NewFailureError("chat completions SSE error", *chunk.Error)
	}
	if chunk.Usage != nil {
		decoder.usage = chunk.Usage
	}

	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue
		}
		if err := decoder.consumeDelta(choice.Delta); err != nil {
			return false, err
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			decoder.finishReason = *choice.FinishReason
		}
	}
	return false, nil
}

func (decoder *streamDecoder) consumeDelta(delta completionDelta) error {
	visible := delta.Content
	if visible == "" {
		visible = delta.Refusal
	}
	decoder.text.WriteString(visible)
	if err := decoder.observer.Text(visible); err != nil {
		return err
	}

	reasoning := delta.ReasoningContent
	if reasoning == "" {
		reasoning = delta.Reasoning
	}
	decoder.reasoning.WriteString(reasoning)
	if err := decoder.observer.Reasoning(reasoning); err != nil {
		return err
	}

	for _, deltaCall := range delta.ToolCalls {
		call := decoder.toolCalls.apply(deltaCall)
		if err := decoder.deliverToolCall(call, false); err != nil {
			return err
		}
	}
	return nil
}

func (decoder *streamDecoder) deliverToolCall(call streamedToolCall, complete bool) error {
	if call.id == "" || call.name == "" {
		return nil
	}
	return decoder.observer.ToolCall(call.id, call.name, call.arguments, complete)
}

func (decoder *streamDecoder) finish() (protocol.Result, error) {
	if decoder.finishReason == "" {
		return protocol.Result{}, backendhttp.ErrIncompleteStream
	}
	switch decoder.finishReason {
	case "stop", "tool_calls", "content_filter":
	case "length":
		return protocol.Result{}, fmt.Errorf("chat completion stopped with finish reason %q", decoder.finishReason)
	default:
		return protocol.Result{}, fmt.Errorf("chat completion has unsupported finish reason %q", decoder.finishReason)
	}

	finalizedCalls, err := decoder.toolCalls.finalize()
	if err != nil {
		return protocol.Result{}, err
	}
	calls := make([]agent.ToolCall, 0, len(finalizedCalls))
	wireCalls := make([]toolCall, 0, len(finalizedCalls))
	for _, finalized := range finalizedCalls {
		if err := decoder.deliverToolCall(finalized.streamed, true); err != nil {
			return protocol.Result{}, err
		}
		calls = append(calls, finalized.call)
		wireCalls = append(wireCalls, finalized.wire)
	}

	text := decoder.text.String()
	var content any
	if text != "" {
		content = text
	}
	assistant, _ := json.Marshal(assistantMessage{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoningContent(decoder.reasoning.String(), decoder.serializeReasoningContent),
		ToolCalls:        wireCalls,
	})

	return protocol.Result{
		Text:      text,
		ToolCalls: calls,
		Output:    []json.RawMessage{assistant},
		Usage:     normalizeUsage(decoder.usage),
	}, nil
}

func normalizeUsage(usage *completionUsage) agent.Usage {
	if usage == nil {
		return agent.Usage{}
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	return agent.Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  total,
	}
}
