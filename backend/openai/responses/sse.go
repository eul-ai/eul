package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type responseStreamResult struct {
	response     createResponseEnvelope
	sawTextDelta bool
}

type responseStreamEvent struct {
	Type        string                   `json:"type"`
	OutputIndex int                      `json:"output_index"`
	Response    json.RawMessage          `json:"response"`
	Item        json.RawMessage          `json:"item"`
	Delta       string                   `json:"delta"`
	Arguments   string                   `json:"arguments"`
	Error       *backendhttp.APIError    `json:"error"`
	Code        backendhttp.APIErrorCode `json:"code"`
	Message     string                   `json:"message"`
}

type streamedOutputItem struct {
	index int
	item  json.RawMessage
}

type responseStreamDecoder struct {
	observer     backendhttp.Observer
	sawText      bool
	sawReasoning bool
	response     createResponseEnvelope
	output       []streamedOutputItem
	toolCalls    toolCallAccumulator
}

func readResponsesSSE(reader io.Reader, observer agent.StreamObserver) (responseStreamResult, error) {
	decoder := responseStreamDecoder{observer: backendhttp.NewObserver(observer), toolCalls: newToolCallAccumulator()}
	done, err := backendhttp.ReadSSE(reader, decoder.handleData)
	if err != nil {
		return responseStreamResult{}, fmt.Errorf("read Responses SSE: %w", err)
	}
	if !done {
		return responseStreamResult{}, backendhttp.ErrIncompleteStream
	}
	return responseStreamResult{response: decoder.response, sawTextDelta: decoder.sawText}, nil
}

func (decoder *responseStreamDecoder) handleData(data []byte) (bool, error) {
	if bytes.Equal(data, []byte("[DONE]")) {
		return false, nil
	}
	response, complete, err := decoder.handleEvent(data)
	if complete {
		decoder.response = response
	}
	return complete, err
}

func (decoder *responseStreamDecoder) handleEvent(data []byte) (createResponseEnvelope, bool, error) {
	var event responseStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return createResponseEnvelope{}, false, fmt.Errorf("decode Responses SSE event: %w", err)
	}

	switch event.Type {
	case "error":
		return createResponseEnvelope{}, false, streamError(event)
	case "response.output_text.delta", "response.refusal.delta":
		decoder.sawText = decoder.sawText || event.Delta != ""
		return createResponseEnvelope{}, false, decoder.observer.Text(event.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		decoder.sawReasoning = decoder.sawReasoning || event.Delta != ""
		return createResponseEnvelope{}, false, decoder.observer.Reasoning(event.Delta)
	case "response.reasoning_summary_part.done":
		if !decoder.sawReasoning {
			return createResponseEnvelope{}, false, nil
		}
		return createResponseEnvelope{}, false, decoder.observer.Reasoning("\n\n")
	case "response.output_item.added":
		return createResponseEnvelope{}, false, decoder.startToolCall(event)
	case "response.function_call_arguments.delta":
		return createResponseEnvelope{}, false, decoder.updateToolCall(event, false)
	case "response.function_call_arguments.done":
		return createResponseEnvelope{}, false, decoder.updateToolCall(event, true)
	case "response.output_item.done":
		if err := decoder.finishToolCall(event); err != nil {
			return createResponseEnvelope{}, false, err
		}
		decoder.output = append(decoder.output, streamedOutputItem{index: event.OutputIndex, item: event.Item})
		return createResponseEnvelope{}, false, nil
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		response, err := decoder.terminal(event)
		return response, err == nil, err
	default:
		return createResponseEnvelope{}, false, nil
	}
}

func (decoder *responseStreamDecoder) startToolCall(event responseStreamEvent) error {
	streamed, deliver := decoder.toolCalls.start(event.OutputIndex, event.Item)
	if !deliver {
		return nil
	}
	return decoder.observer.ToolCall(streamed.id, streamed.name, streamed.arguments, false)
}

func (decoder *responseStreamDecoder) updateToolCall(event responseStreamEvent, complete bool) error {
	streamed, deliver := decoder.toolCalls.update(event.OutputIndex, event.Delta, event.Arguments, complete)
	if !deliver {
		return nil
	}
	return decoder.observer.ToolCall(streamed.id, streamed.name, streamed.arguments, complete)
}

func (decoder *responseStreamDecoder) finishToolCall(event responseStreamEvent) error {
	streamed, deliver := decoder.toolCalls.finish(event.OutputIndex, event.Item)
	if !deliver {
		return nil
	}
	return decoder.observer.ToolCall(streamed.id, streamed.name, streamed.arguments, true)
}

func (decoder *responseStreamDecoder) terminal(event responseStreamEvent) (createResponseEnvelope, error) {
	if len(event.Response) == 0 || bytes.Equal(bytes.TrimSpace(event.Response), []byte("null")) {
		return createResponseEnvelope{}, errors.New("responses terminal SSE event is missing response")
	}

	var response createResponseEnvelope
	if err := json.Unmarshal(event.Response, &response); err != nil {
		return createResponseEnvelope{}, fmt.Errorf("decode Responses terminal response: %w", err)
	}
	if response.Status == "" {
		switch event.Type {
		case "response.completed", "response.done":
			response.Status = "completed"
		case "response.incomplete":
			response.Status = "incomplete"
		case "response.failed":
			response.Status = "failed"
		}
	}

	switch {
	case len(response.Output) == 0 && len(decoder.output) != 0:
		sort.SliceStable(decoder.output, func(i, j int) bool {
			return decoder.output[i].index < decoder.output[j].index
		})
		response.Output = make([]json.RawMessage, len(decoder.output))
		for index, item := range decoder.output {
			response.Output[index] = item.item
		}
	case response.Output == nil:
		response.Output = []json.RawMessage{}
	}
	return response, nil
}

func streamError(event responseStreamEvent) error {
	detail := backendhttp.APIError{Code: event.Code, Message: event.Message}
	if event.Error != nil && backendhttp.FormatAPIError(*event.Error) != "" {
		detail = *event.Error
	}
	return backendhttp.NewFailureError("responses SSE error", detail)
}
