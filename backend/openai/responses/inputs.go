package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	return encodeInputsWithOptions(inputs, false)
}

func encodeInputsWithOptions(inputs []agent.Input, encodeInboxAsAgentMessage bool) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("input %d: %w", index, err)
		}

		var value any
		switch input.Kind {
		case agent.InputUser:
			value = inputMessage{Role: "user", Content: encodeUserContent(input.Content)}
		case agent.InputInbox:
			if encodeInboxAsAgentMessage {
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
		}
		items[index], _ = json.Marshal(value)
	}
	return items, nil
}

func encodeUserContent(content []agent.ContentPart) any {
	parts := make([]inputContentPart, 0, len(content))
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			parts = append(parts, inputContentPart{Type: "input_text", Text: part.Text})
		case agent.ContentPartImage:
			image := part.Image
			if image == nil {
				continue
			}
			parts = append(parts, inputContentPart{
				Type:     "input_image",
				ImageURL: "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
			})
		}
	}
	return parts
}
