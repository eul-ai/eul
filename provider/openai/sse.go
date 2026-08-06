package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type streamObserver struct {
	onText   func(string) error
	sawDelta bool
}

type responseStreamEvent struct {
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response"`
	Item     json.RawMessage `json:"item"`
	Delta    string          `json:"delta"`
	Error    *responseError  `json:"error"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
}

type responseStreamDecoder struct {
	observer *streamObserver
	output   []json.RawMessage
}

// readResponsesSSE consumes a bounded Responses API stream until its terminal
// event. Completed items are retained for tool calls and continuation replay.
func readResponsesSSE(reader io.Reader, maximum int64, observer *streamObserver) (createResponseEnvelope, error) {
	decoder := responseStreamDecoder{observer: observer}
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
			return createResponseEnvelope{}, fmt.Errorf("Responses SSE response exceeds %d bytes", maximum)
		}

		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			response, done, handleErr := handleSSEData(dataLines, handle)
			dataLines = nil
			if handleErr != nil {
				return createResponseEnvelope{}, handleErr
			}
			if done {
				return response, nil
			}
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data := line[len("data:"):]
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			dataLines = append(dataLines, append([]byte(nil), data...))
		}

		if errors.Is(err, io.EOF) {
			response, done, handleErr := handleSSEData(dataLines, handle)
			if handleErr != nil {
				return createResponseEnvelope{}, handleErr
			}
			if done {
				return response, nil
			}
			return createResponseEnvelope{}, errors.New("Responses SSE stream ended without a terminal response")
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
		return createResponseEnvelope{}, false, decoder.deliver(event.Delta)
	case "response.output_item.done":
		if err := validateRawObject(event.Item); err != nil {
			return createResponseEnvelope{}, false, fmt.Errorf("Responses completed output item: %w", err)
		}
		decoder.output = append(decoder.output, append(json.RawMessage(nil), event.Item...))
		return createResponseEnvelope{}, false, nil
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		response, err := decoder.terminal(event)
		return response, err == nil, err
	default:
		return createResponseEnvelope{}, false, nil
	}
}

func (decoder *responseStreamDecoder) deliver(delta string) error {
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

func (decoder *responseStreamDecoder) terminal(event responseStreamEvent) (createResponseEnvelope, error) {
	if len(event.Response) == 0 || bytes.Equal(bytes.TrimSpace(event.Response), []byte("null")) {
		return createResponseEnvelope{}, errors.New("Responses terminal SSE event is missing response")
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
	if len(response.Output) == 0 && len(decoder.output) != 0 {
		response.Output = append([]json.RawMessage(nil), decoder.output...)
	} else if response.Output == nil {
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
	return errors.New("Responses SSE error: " + detail)
}
