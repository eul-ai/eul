package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

func (client *Client) encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		var value any
		switch input.Kind {
		case agent.InputUser:
			content, err := encodeUserContent(input.Content)
			if err != nil {
				return nil, err
			}
			value = inputMessage{Role: "user", Content: content}
		case agent.InputInbox:
			if client.encodeInboxAsAgentMessage {
				value = inputAgentMessage{
					Type:      "agent_message",
					Author:    "/root/subagents",
					Recipient: "/root",
					Content:   []agentMessageInputContent{{Type: "input_text", Text: input.Text}},
				}
			} else {
				value = inputMessage{Role: "user", Content: input.Text}
			}
		case agent.InputToolResult:
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = functionCallOutput{Type: "function_call_output", CallID: input.CallID, Output: output}
		default:
			return nil, fmt.Errorf("unknown input kind %q", input.Kind)
		}
		items[index], _ = json.Marshal(value)
	}
	return items, nil
}

func encodeUserContent(content []agent.ContentPart) ([]inputContentPart, error) {
	parts := make([]inputContentPart, 0, len(content))
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			parts = append(parts, inputContentPart{Type: "input_text", Text: part.Text})
		case agent.ContentPartImage:
			parts = append(parts, inputContentPart{
				Type:     "input_image",
				ImageURL: "data:" + part.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data),
			})
		default:
			return nil, fmt.Errorf("unknown content kind %q", part.Kind)
		}
	}
	return parts, nil
}
