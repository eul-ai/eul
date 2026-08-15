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

type observerDeliveryError struct {
	operation string
	cause     error
}

func (e *observerDeliveryError) Error() string { return e.operation + ": " + e.cause.Error() }
func (e *observerDeliveryError) Unwrap() error { return e.cause }

type partialResponseError struct {
	cause error
}

func (e *partialResponseError) Error() string { return e.cause.Error() }
func (e *partialResponseError) Unwrap() error { return e.cause }

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

type streamedToolCall struct {
	id        string
	name      string
	arguments string
	complete  bool
}

type streamedOutputItem struct {
	index int
	item  json.RawMessage
}

type responseStreamDecoder struct {
	observer    *streamObserver
	output      []streamedOutputItem
	toolStreams map[int]streamedToolCall
}

func readResponsesSSE(reader io.Reader, maximum int64, observer *streamObserver) (createResponseEnvelope, error) {
	decoder := responseStreamDecoder{observer: observer, toolStreams: make(map[int]streamedToolCall)}
	response, err := readSSE(reader, maximum, decoder.handle)
	return response, decoder.wrapPartial(err)
}

func readSSE(reader io.Reader, maximum int64, handle func([]byte) (createResponseEnvelope, bool, error)) (createResponseEnvelope, error) {
	var response createResponseEnvelope
	done, err := backendhttp.ReadSSE(reader, maximum, func(data []byte) (bool, error) {
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return false, nil
		}
		result, complete, err := handle(data)
		if complete {
			response = result
		}
		return complete, err
	})
	if err != nil {
		return createResponseEnvelope{}, fmt.Errorf("read Responses SSE: %w", err)
	}
	if !done {
		return createResponseEnvelope{}, errResponsesSSEIncomplete
	}
	return response, nil
}

func (decoder *responseStreamDecoder) handle(data []byte) (createResponseEnvelope, bool, error) {
	var event responseStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return createResponseEnvelope{}, false, fmt.Errorf("decode Responses SSE event: %w", err)
	}

	switch event.Type {
	case "error":
		return createResponseEnvelope{}, false, streamError(event)
	case "response.output_text.delta", "response.refusal.delta":
		return createResponseEnvelope{}, false, decoder.deliverText(event.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return createResponseEnvelope{}, false, decoder.deliverReasoning(event.Delta)
	case "response.reasoning_summary_part.done":
		if decoder.observer == nil || !decoder.observer.sawReasoning {
			return createResponseEnvelope{}, false, nil
		}
		return createResponseEnvelope{}, false, decoder.deliverReasoning("\n\n")
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
	var item outputItem
	if len(event.Item) == 0 || json.Unmarshal(event.Item, &item) != nil || item.Type != "function_call" {
		return nil
	}
	if item.CallID == "" || item.Name == "" {
		return nil
	}

	streamed := streamedToolCall{id: item.CallID, name: item.Name, arguments: item.Arguments}
	decoder.toolStreams[event.OutputIndex] = streamed
	return decoder.deliverToolCall(streamed, false)
}

func (decoder *responseStreamDecoder) updateToolCall(event responseStreamEvent, complete bool) error {
	streamed, exists := decoder.toolStreams[event.OutputIndex]
	if !exists {
		return nil
	}
	previousArguments := streamed.arguments
	if complete {
		if event.Arguments != "" {
			streamed.arguments = event.Arguments
		}
		if streamed.complete && streamed.arguments == previousArguments {
			return nil
		}
		streamed.complete = true
	} else {
		streamed.arguments += event.Delta
		if streamed.arguments == previousArguments {
			return nil
		}
	}
	decoder.toolStreams[event.OutputIndex] = streamed
	return decoder.deliverToolCall(streamed, complete)
}

func (decoder *responseStreamDecoder) finishToolCall(event responseStreamEvent) error {
	var item outputItem
	if json.Unmarshal(event.Item, &item) != nil || item.Type != "function_call" {
		return nil
	}
	streamed, exists := decoder.toolStreams[event.OutputIndex]
	if !exists {
		streamed = streamedToolCall{id: item.CallID, name: item.Name}
	}
	previousArguments := streamed.arguments
	wasComplete := streamed.complete
	if item.CallID != "" {
		streamed.id = item.CallID
	}
	if item.Name != "" {
		streamed.name = item.Name
	}
	if item.Arguments != "" {
		streamed.arguments = item.Arguments
	}
	delete(decoder.toolStreams, event.OutputIndex)
	if streamed.id == "" || streamed.name == "" || wasComplete && streamed.arguments == previousArguments {
		return nil
	}
	return decoder.deliverToolCall(streamed, true)
}

func (decoder *responseStreamDecoder) deliverToolCall(streamed streamedToolCall, complete bool) error {
	if decoder.observer == nil || decoder.observer.observer.ToolCall == nil {
		return nil
	}
	snapshot := agent.ToolCallSnapshot{
		ID:           streamed.id,
		Name:         streamed.name,
		RawArguments: streamed.arguments,
		Complete:     complete,
	}
	if err := decoder.observer.observer.ToolCall(snapshot); err != nil {
		return &observerDeliveryError{operation: "deliver tool call", cause: err}
	}
	decoder.observer.observed = true
	return nil
}

func (decoder *responseStreamDecoder) deliverText(delta string) error {
	if delta == "" || decoder.observer == nil {
		return nil
	}

	if decoder.observer.observer.Text != nil {
		if err := decoder.observer.observer.Text(delta); err != nil {
			return &observerDeliveryError{operation: "deliver text", cause: err}
		}
		decoder.observer.observed = true
	}

	decoder.observer.sawDelta = true
	return nil
}

func (decoder *responseStreamDecoder) deliverReasoning(delta string) error {
	if delta == "" || decoder.observer == nil {
		return nil
	}

	if decoder.observer.observer.Reasoning != nil {
		if err := decoder.observer.observer.Reasoning(delta); err != nil {
			return &observerDeliveryError{operation: "deliver reasoning", cause: err}
		}
		decoder.observer.observed = true
	}

	decoder.observer.sawReasoning = true
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

func (decoder *responseStreamDecoder) wrapPartial(err error) error {
	if err == nil || decoder.observer == nil || !decoder.observer.observed {
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
