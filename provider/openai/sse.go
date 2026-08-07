package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"yaah/agent"
)

type streamObserver struct {
	onText       func(string) error
	onReasoning  func(string) error
	onToolCall   agent.ToolCallSink
	sawDelta     bool
	sawReasoning bool
}

type responseStreamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Response    json.RawMessage `json:"response"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Error       *responseError  `json:"error"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
}

type streamedToolCall struct {
	id        string
	name      string
	arguments string
	complete  bool
}

type responseStreamDecoder struct {
	observer    *streamObserver
	output      []json.RawMessage
	toolStreams map[int]streamedToolCall
}

func readResponsesSSE(reader io.Reader, maximum int64, observer *streamObserver) (createResponseEnvelope, error) {
	decoder := responseStreamDecoder{observer: observer, toolStreams: make(map[int]streamedToolCall)}
	return readSSE(reader, maximum, decoder.handle)
}

func readSSE(reader io.Reader, maximum int64, handle func([]byte) (createResponseEnvelope, bool, error)) (createResponseEnvelope, error) {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	buffered := bufio.NewReader(limited)
	var dataLines [][]byte

	for {
		line, err := buffered.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return createResponseEnvelope{}, fmt.Errorf("read Responses SSE: %w", err)
		}
		if limited.N == 0 {
			return createResponseEnvelope{}, fmt.Errorf("responses SSE response exceeds %d bytes", maximum)
		}

		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case len(line) == 0:
			response, done, handleErr := handleSSEData(dataLines, handle)
			dataLines = nil
			if handleErr != nil {
				return createResponseEnvelope{}, handleErr
			}
			if done {
				return response, nil
			}
		case bytes.HasPrefix(line, []byte("data:")):
			data := line[len("data:"):]
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			dataLines = append(dataLines, data)
		}

		if errors.Is(err, io.EOF) {
			response, done, handleErr := handleSSEData(dataLines, handle)
			if handleErr != nil {
				return createResponseEnvelope{}, handleErr
			}
			if done {
				return response, nil
			}
			return createResponseEnvelope{}, errors.New("responses SSE stream ended without a terminal response")
		}
	}
}

func handleSSEData(dataLines [][]byte, handle func([]byte) (createResponseEnvelope, bool, error)) (createResponseEnvelope, bool, error) {
	if len(dataLines) == 0 {
		return createResponseEnvelope{}, false, nil
	}

	data := bytes.Join(dataLines, []byte("\n"))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return createResponseEnvelope{}, false, nil
	}

	return handle(data)
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
		decoder.output = append(decoder.output, event.Item)
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
	if decoder.observer == nil || decoder.observer.onToolCall == nil {
		return nil
	}
	snapshot := agent.ToolCallSnapshot{
		ID:           streamed.id,
		Name:         streamed.name,
		RawArguments: streamed.arguments,
		Arguments:    parseStreamingJSONObject(streamed.arguments),
		Complete:     complete,
	}
	if err := decoder.observer.onToolCall(snapshot); err != nil {
		return fmt.Errorf("deliver tool call: %w", err)
	}
	return nil
}

func (decoder *responseStreamDecoder) deliverText(delta string) error {
	if delta == "" || decoder.observer == nil {
		return nil
	}

	if decoder.observer.onText != nil {
		if err := decoder.observer.onText(delta); err != nil {
			return fmt.Errorf("deliver text: %w", err)
		}
	}

	decoder.observer.sawDelta = true
	return nil
}

func (decoder *responseStreamDecoder) deliverReasoning(delta string) error {
	if delta == "" || decoder.observer == nil {
		return nil
	}

	if decoder.observer.onReasoning != nil {
		if err := decoder.observer.onReasoning(delta); err != nil {
			return fmt.Errorf("deliver reasoning: %w", err)
		}
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
		response.Output = decoder.output
	case response.Output == nil:
		response.Output = []json.RawMessage{}
	}
	return response, nil
}

func streamError(event responseStreamEvent) error {
	detail := ""
	if event.Error != nil {
		detail = formatResponseError(*event.Error)
	}

	if detail == "" {
		detail = formatResponseError(responseError{Code: event.Code, Message: event.Message})
	}

	if detail == "" {
		detail = "unspecified error"
	}

	return errors.New("responses SSE error: " + detail)
}
