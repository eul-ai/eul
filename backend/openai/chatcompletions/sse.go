package chatcompletions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/eul-ai/eul/agent"
)

var errSSEIncomplete = errors.New("chat completions SSE stream ended without a terminal event")

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

type completionChunk struct {
	Choices []completionChoice `json:"choices"`
	Usage   *completionUsage   `json:"usage"`
	Error   *apiError          `json:"error"`
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

type streamedToolCall struct {
	index     int
	id        string
	name      string
	arguments string
}

type streamResult struct {
	text      string
	reasoning string
	calls     []agent.ToolCall
	assistant json.RawMessage
	usage     agent.Usage
}

type streamDecoder struct {
	observer     agent.StreamObserver
	text         strings.Builder
	reasoning    strings.Builder
	toolCalls    map[int]streamedToolCall
	usage        *completionUsage
	finishReason string
	observed     bool
}

func readCompletionSSE(reader io.Reader, maximum int64, observer agent.StreamObserver) (streamResult, error) {
	decoder := streamDecoder{observer: observer, toolCalls: make(map[int]streamedToolCall)}
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	buffered := bufio.NewReader(limited)
	var dataLines [][]byte

	for {
		line, err := buffered.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return streamResult{}, decoder.wrapPartial(fmt.Errorf("read Chat Completions SSE: %w", err))
		}
		if limited.N == 0 {
			return streamResult{}, decoder.wrapPartial(fmt.Errorf("chat completions SSE response exceeds %d bytes", maximum))
		}

		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case len(line) == 0:
			done, handleErr := decoder.handleData(dataLines)
			dataLines = nil
			if handleErr != nil {
				return streamResult{}, decoder.wrapPartial(handleErr)
			}
			if done {
				result, finishErr := decoder.finish()
				return result, decoder.wrapPartial(finishErr)
			}
		case bytes.HasPrefix(line, []byte("data:")):
			data := line[len("data:"):]
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			dataLines = append(dataLines, data)
		}

		if errors.Is(err, io.EOF) {
			done, handleErr := decoder.handleData(dataLines)
			if handleErr != nil {
				return streamResult{}, decoder.wrapPartial(handleErr)
			}
			if done || decoder.finishReason != "" {
				result, finishErr := decoder.finish()
				return result, decoder.wrapPartial(finishErr)
			}
			return streamResult{}, decoder.wrapPartial(errSSEIncomplete)
		}
	}
}

func (decoder *streamDecoder) handleData(lines [][]byte) (bool, error) {
	if len(lines) == 0 {
		return false, nil
	}
	data := bytes.Join(lines, []byte("\n"))
	if len(data) == 0 {
		return false, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return true, nil
	}

	var chunk completionChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return false, fmt.Errorf("decode Chat Completions SSE event: %w", err)
	}
	if chunk.Error != nil {
		return false, &responseFailureError{message: "chat completions SSE error: " + formatAPIError(*chunk.Error), detail: *chunk.Error}
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
	if visible != "" {
		decoder.text.WriteString(visible)
		decoder.observed = true
		if decoder.observer.Text != nil {
			if err := decoder.observer.Text(visible); err != nil {
				return &observerDeliveryError{operation: "deliver text", cause: err}
			}
		}
	}

	reasoning := delta.ReasoningContent
	if reasoning == "" {
		reasoning = delta.Reasoning
	}
	if reasoning != "" {
		decoder.reasoning.WriteString(reasoning)
		decoder.observed = true
		if decoder.observer.Reasoning != nil {
			if err := decoder.observer.Reasoning(reasoning); err != nil {
				return &observerDeliveryError{operation: "deliver reasoning", cause: err}
			}
		}
	}

	for _, deltaCall := range delta.ToolCalls {
		call, exists := decoder.toolCalls[deltaCall.Index]
		call.index = deltaCall.Index
		if deltaCall.ID != "" {
			call.id = deltaCall.ID
		}
		if deltaCall.Function.Name != "" {
			call.name = deltaCall.Function.Name
		}
		if !exists && (call.id == "" || call.name == "") {
			return fmt.Errorf("chat completion tool call %d starts without an ID and name", deltaCall.Index)
		}
		call.arguments += deltaCall.Function.Arguments
		decoder.toolCalls[deltaCall.Index] = call
		decoder.observed = true
		if err := decoder.deliverToolCall(call, false); err != nil {
			return err
		}
	}
	return nil
}

func (decoder *streamDecoder) deliverToolCall(call streamedToolCall, complete bool) error {
	if decoder.observer.ToolCall == nil || call.id == "" || call.name == "" {
		return nil
	}
	if err := decoder.observer.ToolCall(agent.ToolCallSnapshot{
		ID:           call.id,
		Name:         call.name,
		RawArguments: call.arguments,
		Complete:     complete,
	}); err != nil {
		return &observerDeliveryError{operation: "deliver tool call", cause: err}
	}
	return nil
}

func (decoder *streamDecoder) finish() (streamResult, error) {
	if decoder.finishReason == "" {
		return streamResult{}, errSSEIncomplete
	}
	switch decoder.finishReason {
	case "stop", "tool_calls", "function_call", "content_filter":
	case "length":
		return streamResult{}, fmt.Errorf("chat completion stopped with finish reason %q", decoder.finishReason)
	default:
		return streamResult{}, fmt.Errorf("chat completion has unsupported finish reason %q", decoder.finishReason)
	}

	indexes := make([]int, 0, len(decoder.toolCalls))
	for index := range decoder.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	calls := make([]agent.ToolCall, 0, len(indexes))
	wireCalls := make([]toolCall, 0, len(indexes))
	seen := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		call := decoder.toolCalls[index]
		if call.id == "" || call.name == "" {
			return streamResult{}, fmt.Errorf("chat completion tool call %d is incomplete", index)
		}
		if _, exists := seen[call.id]; exists {
			return streamResult{}, fmt.Errorf("chat completion has duplicate tool call ID %q", call.id)
		}
		seen[call.id] = struct{}{}
		if err := decoder.deliverToolCall(call, true); err != nil {
			return streamResult{}, err
		}
		arguments := call.arguments
		if arguments == "" {
			arguments = "{}"
		}
		calls = append(calls, agent.ToolCall{ID: call.id, Name: call.name, Arguments: json.RawMessage(arguments)})
		wireCalls = append(wireCalls, toolCall{
			ID:   call.id,
			Type: "function",
			Function: toolFunction{
				Name:      call.name,
				Arguments: arguments,
			},
		})
	}

	text := decoder.text.String()
	reasoning := decoder.reasoning.String()
	var content any
	if text != "" {
		content = text
	}
	assistant, err := json.Marshal(assistantMessage{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		ToolCalls:        wireCalls,
	})
	if err != nil {
		return streamResult{}, fmt.Errorf("encode assistant message: %w", err)
	}

	usage, err := normalizeUsage(decoder.usage)
	if err != nil {
		return streamResult{}, err
	}
	return streamResult{
		text:      text,
		reasoning: reasoning,
		calls:     calls,
		assistant: assistant,
		usage:     usage,
	}, nil
}

func normalizeUsage(usage *completionUsage) (agent.Usage, error) {
	if usage == nil {
		return agent.Usage{}, nil
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return agent.Usage{}, errors.New("chat completion usage contains negative token counts")
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	return agent.Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  total,
	}, nil
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
