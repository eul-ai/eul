package chatcompletions

import (
	"encoding/json"

	"github.com/eul-ai/eul/agent"
)

func buildRequest(request agent.Request, maxStateBytes, generationStateBytes int) (createRequest, []json.RawMessage, []json.RawMessage, error) {
	created, history, newMessages, err := buildRequestUnchecked(request, maxStateBytes)
	if err != nil {
		return createRequest{}, nil, nil, err
	}

	if _, err := encodeState(history, newMessages, nil, generationStateBytes); err != nil {
		return createRequest{}, nil, nil, err
	}
	return created, history, newMessages, nil
}

func buildRequestUnchecked(request agent.Request, maxStateBytes int) (createRequest, []json.RawMessage, []json.RawMessage, error) {
	history, err := decodeState(request.State, maxStateBytes)
	if err != nil {
		return createRequest{}, nil, nil, err
	}

	newMessages, err := encodeInputs(request.Inputs)
	if err != nil {
		return createRequest{}, nil, nil, err
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

	return createRequest{
		Model:    request.Model,
		Messages: messages,
		Tools:    tools,
	}, history, newMessages, nil
}
