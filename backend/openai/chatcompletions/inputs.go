package chatcompletions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	messages := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("input %d: %w", index, err)
		}

		var value message
		switch input.Kind {
		case agent.InputUser:
			value = message{Role: "user", Content: encodeUserContent(input.Content)}
		case agent.InputInbox:
			value = message{Role: "user", Content: input.Text}
		case agent.InputToolResult:
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = message{Role: "tool", Content: output, ToolCallID: input.CallID}
		}
		messages[index], _ = json.Marshal(value)
	}
	return messages, nil
}

func encodeUserContent(content []agent.ContentPart) any {
	var text string
	hasImage := false
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			text += part.Text
		case agent.ContentPartImage:
			hasImage = true
		}
	}
	if !hasImage {
		return text
	}

	parts := make([]contentPart, 0, len(content))
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			parts = append(parts, contentPart{Type: "text", Text: part.Text})
		case agent.ContentPartImage:
			if part.Image == nil {
				continue
			}
			parts = append(parts, contentPart{
				Type: "image_url",
				ImageURL: &imageURL{
					URL: "data:" + part.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data),
				},
			})
		}
	}
	return parts
}
