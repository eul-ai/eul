package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// readCodexSSE consumes bounded Codex SSE events until the terminal response.
// Completed output items arrive separately from response.completed, so they are
// collected for normalization and continuation replay.
func readCodexSSE(reader io.Reader, maximum int64) (createResponseEnvelope, error) {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	buffered := bufio.NewReader(limited)
	var dataLines [][]byte
	var output []json.RawMessage

	processEvent := func() (createResponseEnvelope, bool, error) {
		if len(dataLines) == 0 {
			return createResponseEnvelope{}, false, nil
		}
		data := bytes.Join(dataLines, []byte("\n"))
		dataLines = nil
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return createResponseEnvelope{}, false, nil
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Error    *responseError  `json:"error"`
			Code     string          `json:"code"`
			Message  string          `json:"message"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return createResponseEnvelope{}, false, fmt.Errorf("decode Codex SSE event: %w", err)
		}
		switch event.Type {
		case "error":
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
			return createResponseEnvelope{}, false, errors.New("Codex SSE error: " + detail)
		case "response.output_item.done":
			if err := validateRawObject(event.Item); err != nil {
				return createResponseEnvelope{}, false, fmt.Errorf("Codex completed output item: %w", err)
			}
			output = append(output, append(json.RawMessage(nil), event.Item...))
			return createResponseEnvelope{}, false, nil
		case "response.completed", "response.done", "response.incomplete", "response.failed":
			if len(event.Response) == 0 || bytes.Equal(bytes.TrimSpace(event.Response), []byte("null")) {
				return createResponseEnvelope{}, false, errors.New("Codex terminal SSE event is missing response")
			}
			decoded, err := decodeCreateResponse(event.Response)
			if err != nil {
				return createResponseEnvelope{}, false, fmt.Errorf("decode Codex terminal response: %w", err)
			}
			if decoded.Status == "" {
				switch event.Type {
				case "response.completed", "response.done":
					decoded.Status = "completed"
				case "response.incomplete":
					decoded.Status = "incomplete"
				case "response.failed":
					decoded.Status = "failed"
				}
			}
			if len(decoded.Output) == 0 && len(output) != 0 {
				decoded.Output = append([]json.RawMessage(nil), output...)
			} else if decoded.Output == nil {
				decoded.Output = []json.RawMessage{}
			}
			return decoded, true, nil
		default:
			return createResponseEnvelope{}, false, nil
		}
	}

	for {
		line, err := buffered.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return createResponseEnvelope{}, fmt.Errorf("read Codex SSE: %w", err)
		}
		if limited.N == 0 {
			return createResponseEnvelope{}, fmt.Errorf("Codex SSE response exceeds %d bytes", maximum)
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			response, done, processErr := processEvent()
			if processErr != nil {
				return createResponseEnvelope{}, processErr
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
			response, done, processErr := processEvent()
			if processErr != nil {
				return createResponseEnvelope{}, processErr
			}
			if done {
				return response, nil
			}
			return createResponseEnvelope{}, errors.New("Codex SSE stream ended without a terminal response")
		}
	}
}
