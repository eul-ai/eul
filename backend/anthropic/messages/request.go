package messages

import (
	"encoding/json"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/continuation"
)

type requestBuild struct {
	wire        createRequest
	history     []json.RawMessage
	newMessages []json.RawMessage
}

func buildWireRequest(request agent.Request) (requestBuild, error) {
	history, err := continuation.Decode(request.State)
	if err != nil {
		return requestBuild{}, err
	}
	newMessages, err := encodeInputs(request.Inputs)
	if err != nil {
		return requestBuild{}, err
	}

	messages := make([]json.RawMessage, 0, len(history)+len(newMessages))
	messages = append(messages, history...)
	messages = append(messages, newMessages...)

	tools := make([]toolDefinition, len(request.Tools))
	for index, definition := range request.Tools {
		tools[index] = toolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: definition.Parameters,
		}
	}

	var system []systemBlock
	if request.Instructions != "" {
		system = []systemBlock{{Type: "text", Text: request.Instructions}}
	}
	return requestBuild{
		wire: createRequest{
			Model:    request.Model,
			System:   system,
			Messages: messages,
			Tools:    tools,
		},
		history:     history,
		newMessages: newMessages,
	}, nil
}
