package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"yaah/agent"
)

type createResponseEnvelope struct {
	Status            string                     `json:"status"`
	Error             *responseError             `json:"error"`
	IncompleteDetails *incompleteResponseDetails `json:"incomplete_details"`
	Output            []json.RawMessage          `json:"output"`
	Usage             *responseUsage             `json:"usage"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

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

func normalizeResponse(response createResponseEnvelope) (string, []agent.ToolCall, agent.Usage, error) {
	if response.Error != nil {
		return "", nil, agent.Usage{}, fmt.Errorf("response failed: %s", formatResponseError(*response.Error))
	}
	if response.Status != "completed" {
		detail := response.Status
		if detail == "" {
			detail = "missing"
		}
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
			detail += ": " + response.IncompleteDetails.Reason
		}
		return "", nil, agent.Usage{}, fmt.Errorf("response status %s", detail)
	}
	if response.Output == nil {
		return "", nil, agent.Usage{}, errors.New("response is missing output")
	}

	var text strings.Builder
	var calls []agent.ToolCall
	seenCallIDs := make(map[string]struct{})
	for i, raw := range response.Output {
		var item outputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return "", nil, agent.Usage{}, fmt.Errorf("decode response output item %d: %w", i, err)
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
			if item.CallID == "" {
				return "", nil, agent.Usage{}, fmt.Errorf("response function call %d has no call ID", i)
			}
			if item.Name == "" {
				return "", nil, agent.Usage{}, fmt.Errorf("response function call %d has no name", i)
			}
			if _, exists := seenCallIDs[item.CallID]; exists {
				return "", nil, agent.Usage{}, fmt.Errorf("response has duplicate function call ID %q", item.CallID)
			}
			seenCallIDs[item.CallID] = struct{}{}
			calls = append(calls, agent.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: append(json.RawMessage(nil), item.Arguments...),
			})
		}
	}

	usage := agent.Usage{}
	if response.Usage != nil {
		if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.TotalTokens < 0 {
			return "", nil, agent.Usage{}, errors.New("response usage contains negative token counts")
		}
		usage = agent.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
	}
	return text.String(), calls, usage, nil
}

func formatResponseError(response responseError) string {
	parts := make([]string, 0, 3)
	if response.Type != "" {
		parts = append(parts, response.Type)
	}
	if response.Code != "" {
		parts = append(parts, response.Code)
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
