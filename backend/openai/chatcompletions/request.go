package chatcompletions

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

func buildGenerationRequest(request agent.Request, maxStateBytes, generationStateBytes int) (requestBuild, error) {
	build, err := buildWireRequest(request, maxStateBytes)
	if err != nil {
		return requestBuild{}, err
	}
	if _, err := continuation.Encode(generationStateBytes, build.history, build.newMessages); err != nil {
		return requestBuild{}, err
	}
	return build, nil
}

func buildWireRequest(request agent.Request, maxStateBytes int) (requestBuild, error) {
	history, err := continuation.Decode(request.State, maxStateBytes)
	if err != nil {
		return requestBuild{}, err
	}

	newMessages, err := encodeInputs(request.Inputs)
	if err != nil {
		return requestBuild{}, err
	}

	messages := make([]json.RawMessage, 0, len(history)+len(newMessages)+1)
	if request.Instructions != "" {
		system, _ := json.Marshal(message{Role: "system", Content: request.Instructions})
		messages = append(messages, system)
	}
	messages = append(messages, history...)
	messages = append(messages, newMessages...)

	tools := make([]functionTool, len(request.Tools))
	for index, definition := range request.Tools {
		tools[index] = functionTool{
			Type: "function",
			Function: toolDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
				Strict:      true,
			},
		}
	}

	return requestBuild{
		wire: createRequest{
			Model:    request.Model,
			Messages: messages,
			Tools:    tools,
		},
		history:     history,
		newMessages: newMessages,
	}, nil
}
