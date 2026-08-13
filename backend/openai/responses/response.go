package responses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type createResponseEnvelope struct {
	Status            string                     `json:"status"`
	Error             *responseError             `json:"error"`
	IncompleteDetails *incompleteResponseDetails `json:"incomplete_details"`
	Output            []json.RawMessage          `json:"output"`
	Usage             *responseUsage             `json:"usage"`
}

type responseError struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
	Type    string    `json:"type"`
}

type errorCode string

func (code *errorCode) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*code = ""
		return nil
	}

	var text string
	if json.Unmarshal(data, &text) == nil {
		*code = errorCode(text)
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*code = errorCode(number.String())
	return nil
}

type responseFailureError struct {
	message string
	detail  responseError
}

func (e *responseFailureError) Error() string { return e.message }

type incompleteResponseDetails struct {
	Reason string `json:"reason"`
}

type responseUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type outputItem struct {
	Type      string              `json:"type"`
	CallID    string              `json:"call_id"`
	Name      string              `json:"name"`
	Arguments string              `json:"arguments"`
	Content   []outputContentPart `json:"content"`
}

type outputContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func decodeCreateResponse(body []byte) (createResponseEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var response createResponseEnvelope

	if err := decoder.Decode(&response); err != nil {
		return createResponseEnvelope{}, fmt.Errorf("decode response: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return createResponseEnvelope{}, errors.New("decode response: multiple JSON values")
		}
		return createResponseEnvelope{}, fmt.Errorf("decode response: %w", err)
	}

	for i, item := range response.Output {
		if err := validateRawObject(item); err != nil {
			return createResponseEnvelope{}, fmt.Errorf("response output item %d: %w", i, err)
		}
	}

	return response, nil
}

func validateCompactOutput(output []json.RawMessage) error {
	if len(output) != 1 {
		return fmt.Errorf("compact response must contain exactly one output item, got %d", len(output))
	}

	var item outputItem
	if err := json.Unmarshal(output[0], &item); err != nil {
		return fmt.Errorf("decode compact response output: %w", err)
	}
	if item.Type != "compaction" {
		return fmt.Errorf("compact response output has type %q", item.Type)
	}

	return nil
}

func normalizeResponse(response createResponseEnvelope) (string, []agent.ToolCall, agent.Usage, error) {
	if err := validateCompletedResponse(response); err != nil {
		return "", nil, agent.Usage{}, err
	}

	text, calls, err := normalizeOutput(response.Output)
	if err != nil {
		return "", nil, agent.Usage{}, err
	}

	usage, err := normalizeUsage(response.Usage)
	if err != nil {
		return "", nil, agent.Usage{}, err
	}

	return text, calls, usage, nil
}

func validateCompletedResponse(response createResponseEnvelope) error {
	if response.Error != nil {
		return &responseFailureError{
			message: "response failed: " + formatResponseError(*response.Error),
			detail:  *response.Error,
		}
	}

	if response.Status != "completed" {
		detail := response.Status
		if detail == "" {
			detail = "missing"
		}
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
			detail += ": " + response.IncompleteDetails.Reason
		}
		return fmt.Errorf("response status %s", detail)
	}

	if response.Output == nil {
		return errors.New("response is missing output")
	}

	return nil
}

func normalizeOutput(output []json.RawMessage) (string, []agent.ToolCall, error) {
	var text strings.Builder
	var calls []agent.ToolCall
	seenCallIDs := make(map[string]struct{})
	for index, raw := range output {
		var item outputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return "", nil, fmt.Errorf("decode response output item %d: %w", index, err)
		}

		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					text.WriteString(part.Text)
				case "refusal":
					text.WriteString(part.Refusal)
				}
			}
		case "function_call":
			call, err := normalizeToolCall(item, index, seenCallIDs)
			if err != nil {
				return "", nil, err
			}
			calls = append(calls, call)
		}
	}
	return text.String(), calls, nil
}

func normalizeToolCall(item outputItem, index int, seen map[string]struct{}) (agent.ToolCall, error) {
	if item.CallID == "" {
		return agent.ToolCall{}, fmt.Errorf("response function call %d has no call ID", index)
	}
	if item.Name == "" {
		return agent.ToolCall{}, fmt.Errorf("response function call %d has no name", index)
	}
	if _, exists := seen[item.CallID]; exists {
		return agent.ToolCall{}, fmt.Errorf("response has duplicate function call ID %q", item.CallID)
	}

	seen[item.CallID] = struct{}{}
	return agent.ToolCall{
		ID:        item.CallID,
		Name:      item.Name,
		Arguments: json.RawMessage(item.Arguments),
	}, nil
}

func normalizeUsage(usage *responseUsage) (agent.Usage, error) {
	if usage == nil {
		return agent.Usage{}, nil
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return agent.Usage{}, errors.New("response usage contains negative token counts")
	}

	return agent.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}, nil
}

func formatResponseError(response responseError) string {
	parts := make([]string, 0, 3)
	if response.Type != "" {
		parts = append(parts, response.Type)
	}
	if response.Code != "" {
		parts = append(parts, string(response.Code))
	}

	prefix := strings.Join(parts, "/")
	if prefix == "" {
		return response.Message
	}
	if response.Message == "" {
		return prefix
	}
	return prefix + ": " + response.Message
}
