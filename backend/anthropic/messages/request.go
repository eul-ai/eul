package messages

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
	return createRequest{
		Model:    request.Model,
		System:   system,
		Messages: messages,
		Tools:    tools,
	}, history, newMessages, nil
}
