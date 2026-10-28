package chatcompletions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	messages := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		var value message
		switch input.Kind {
		case agent.InputUser:
			content, err := encodeUserContent(input.Content)
			if err != nil {
				return nil, err
			}
			value = message{Role: "user", Content: content}
		case agent.InputInbox:
			value = message{Role: "user", Content: input.Text}
		case agent.InputToolResult:
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = message{Role: "tool", Content: output, ToolCallID: input.CallID}
		default:
			return nil, fmt.Errorf("unknown input kind %q", input.Kind)
		}
		messages[index], _ = json.Marshal(value)
	}
	return messages, nil
}

func encodeUserContent(content []agent.ContentPart) (any, error) {
	var text strings.Builder
	parts := make([]contentPart, 0, len(content))
	hasImage := false
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			text.WriteString(part.Text)
			parts = append(parts, contentPart{Type: "text", Text: part.Text})
		case agent.ContentPartImage:
			hasImage = true
			parts = append(parts, contentPart{
				Type: "image_url",
				ImageURL: &imageURL{
					URL: "data:" + part.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data),
				},
			})
		default:
			return nil, fmt.Errorf("unknown content kind %q", part.Kind)
		}
	}
	if !hasImage {
		return text.String(), nil
	}
	return parts, nil
}
