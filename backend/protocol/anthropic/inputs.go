package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	blocks := make([]contentBlock, 0, len(inputs))
	for _, input := range inputs {
		switch input.Kind {
		case agent.InputUser:
			for _, part := range input.Content {
				switch part.Kind {
				case agent.ContentPartText:
					if part.Text != "" {
						blocks = append(blocks, contentBlock{Type: "text", Text: part.Text})
					}
				case agent.ContentPartImage:
					blocks = append(blocks, contentBlock{
						Type: "image",
						Source: &imageSource{
							Type:      "base64",
							MediaType: part.Image.MediaType,
							Data:      base64.StdEncoding.EncodeToString(part.Image.Data),
						},
					})
				default:
					return nil, fmt.Errorf("unknown content kind %q", part.Kind)
				}
			}
		case agent.InputInbox:
			blocks = append(blocks, contentBlock{Type: "text", Text: input.Text})
		case agent.InputToolResult:
			blocks = append(blocks, contentBlock{
				Type:      "tool_result",
				ToolUseID: input.CallID,
				Content:   input.Text,
				IsError:   input.IsError,
			})
		default:
			return nil, fmt.Errorf("unknown input kind %q", input.Kind)
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("inputs contain no Anthropic content blocks")
	}

	message, err := marshalWireMessage("user", blocks)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{message}, nil
}
