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

var errResponsesSSEIncomplete = errors.New("responses SSE stream ended without a terminal response")

type streamObserver struct {
	observer     agent.StreamObserver
	sawDelta     bool
	sawReasoning bool
	observed     bool
}

type responseStreamResult struct {
	response     createResponseEnvelope
	sawTextDelta bool
	observed     bool
}

type responseStreamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Response    json.RawMessage `json:"response"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Error       *responseError  `json:"error"`
	Code        errorCode       `json:"code"`
	Message     string          `json:"message"`
}

type streamedOutputItem struct {
	index int
	item  json.RawMessage
}

type responseStreamDecoder struct {
	observer  *streamObserver
	response  createResponseEnvelope
	output    []streamedOutputItem
	toolCalls toolCallAccumulator
}

func readResponsesSSE(reader io.Reader, maximum int64, observer agent.StreamObserver) (responseStreamResult, error) {
	tracked := &streamObserver{observer: observer}
	decoder := responseStreamDecoder{observer: tracked, toolCalls: newToolCallAccumulator()}
	done, err := backendhttp.ReadSSE(reader, maximum, decoder.handleData)
	if err != nil {
		err = fmt.Errorf("read Responses SSE: %w", err)
	} else if !done {
		err = errResponsesSSEIncomplete
	}
	if err != nil {
		return responseStreamResult{}, tracked.wrapPartial(err)
	}
	return responseStreamResult{
		response:     decoder.response,
		sawTextDelta: tracked.sawDelta,
		observed:     tracked.observed,
	}, nil
}

func (decoder *responseStreamDecoder) handleData(data []byte) (bool, error) {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
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
		return createResponseEnvelope{}, false, decoder.observer.deliverText(event.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return createResponseEnvelope{}, false, decoder.observer.deliverReasoning(event.Delta)
	case "response.reasoning_summary_part.done":
		if !decoder.observer.sawReasoning {
			return createResponseEnvelope{}, false, nil
		}
		return createResponseEnvelope{}, false, decoder.observer.deliverReasoning("\n\n")
	case "response.output_item.added":
		return createResponseEnvelope{}, false, decoder.startToolCall(event)
	case "response.function_call_arguments.delta":
		return createResponseEnvelope{}, false, decoder.updateToolCall(event, false)
	case "response.function_call_arguments.done":
		return createResponseEnvelope{}, false, decoder.updateToolCall(event, true)
	case "response.output_item.done":
		if err := validateRawObject(event.Item); err != nil {
			return createResponseEnvelope{}, false, fmt.Errorf("responses completed output item: %w", err)
		}
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
	return decoder.observer.deliverToolCall(streamed, false)
}

func (decoder *responseStreamDecoder) updateToolCall(event responseStreamEvent, complete bool) error {
	streamed, deliver := decoder.toolCalls.update(event.OutputIndex, event.Delta, event.Arguments, complete)
	if !deliver {
		return nil
	}
	return decoder.observer.deliverToolCall(streamed, complete)
}

func (decoder *responseStreamDecoder) finishToolCall(event responseStreamEvent) error {
	streamed, deliver := decoder.toolCalls.finish(event.OutputIndex, event.Item)
	if !deliver {
		return nil
	}
	return decoder.observer.deliverToolCall(streamed, true)
}

func (observer *streamObserver) deliverToolCall(streamed streamedToolCall, complete bool) error {
	if observer.observer.ToolCall == nil {
		return nil
	}
	snapshot := agent.ToolCallSnapshot{
		ID:           streamed.id,
		Name:         streamed.name,
		RawArguments: streamed.arguments,
		Complete:     complete,
	}
	if err := observer.observer.ToolCall(snapshot); err != nil {
		return &observerDeliveryError{operation: "deliver tool call", cause: err}
	}
	observer.observed = true
	return nil
}

func (observer *streamObserver) deliverText(delta string) error {
	if delta == "" {
		return nil
	}

	if observer.observer.Text != nil {
		if err := observer.observer.Text(delta); err != nil {
			return &observerDeliveryError{operation: "deliver text", cause: err}
		}
		observer.observed = true
	}

	observer.sawDelta = true
	return nil
}

func (observer *streamObserver) deliverReasoning(delta string) error {
	if delta == "" {
		return nil
	}

	if observer.observer.Reasoning != nil {
		if err := observer.observer.Reasoning(delta); err != nil {
			return &observerDeliveryError{operation: "deliver reasoning", cause: err}
		}
		observer.observed = true
	}

	observer.sawReasoning = true
	return nil
}

func (decoder *responseStreamDecoder) terminal(event responseStreamEvent) (createResponseEnvelope, error) {
	if len(event.Response) == 0 || bytes.Equal(bytes.TrimSpace(event.Response), []byte("null")) {
		return createResponseEnvelope{}, errors.New("responses terminal SSE event is missing response")
	}

	response, err := decodeCreateResponse(event.Response)
	if err != nil {
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

func (observer *streamObserver) wrapPartial(err error) error {
	if err == nil || !observer.observed {
		return err
	}
	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return err
	}
	return &partialResponseError{cause: err}
}

func streamError(event responseStreamEvent) error {
	errorDetail := responseError{Code: event.Code, Message: event.Message}
	if event.Error != nil && formatResponseError(*event.Error) != "" {
		errorDetail = *event.Error
	}

	detail := formatResponseError(errorDetail)
	if detail == "" {
		detail = "unspecified error"
	}

	return &responseFailureError{
		message: "responses SSE error: " + detail,
		detail:  errorDetail,
	}
}
