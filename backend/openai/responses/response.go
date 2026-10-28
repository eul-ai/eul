package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type createResponseEnvelope struct {
	Status            string                     `json:"status"`
	Error             *backendhttp.APIError      `json:"error"`
	IncompleteDetails *incompleteResponseDetails `json:"incomplete_details"`
	Output            []json.RawMessage          `json:"output"`
	Usage             *responseUsage             `json:"usage"`
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

func normalizeResponse(response createResponseEnvelope) (string, []agent.ToolCall, agent.Usage, error) {
	if err := validateCompletedResponse(response); err != nil {
		return "", nil, agent.Usage{}, err
	}

	text, calls, err := normalizeOutput(response.Output)
	if err != nil {
		return "", nil, agent.Usage{}, err
	}

	return text, calls, normalizeUsage(response.Usage), nil
}

func validateCompletedResponse(response createResponseEnvelope) error {
	if response.Error != nil {
		return backendhttp.NewFailureError("response failed", *response.Error)
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
			call, err := normalizeToolCall(item, index)
			if err != nil {
				return "", nil, err
			}
			calls = append(calls, call)
		}
	}
	return text.String(), calls, nil
}

func normalizeToolCall(item outputItem, index int) (agent.ToolCall, error) {
	if item.CallID == "" {
		return agent.ToolCall{}, fmt.Errorf("response function call %d has no call ID", index)
	}
	if item.Name == "" {
		return agent.ToolCall{}, fmt.Errorf("response function call %d has no name", index)
	}
	return agent.ToolCall{
		ID:        item.CallID,
		Name:      item.Name,
		Arguments: json.RawMessage(item.Arguments),
	}, nil
}

func normalizeUsage(usage *responseUsage) agent.Usage {
	if usage == nil {
		return agent.Usage{}
	}

	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	return agent.Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  total,
	}
}
