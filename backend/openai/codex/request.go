package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

const continuationStateVersion = 1

type createResponseRequest struct {
	Model             string             `json:"model"`
	Instructions      string             `json:"instructions"`
	Input             []json.RawMessage  `json:"input"`
	Tools             []functionTool     `json:"tools"`
	Store             bool               `json:"store"`
	Stream            bool               `json:"stream"`
	Include           []string           `json:"include"`
	Text              *responseText      `json:"text,omitempty"`
	Reasoning         *responseReasoning `json:"reasoning,omitempty"`
	ToolChoice        string             `json:"tool_choice,omitempty"`
	ParallelToolCalls bool               `json:"parallel_tool_calls,omitempty"`
}

type compactRequest struct {
	Model             string             `json:"model"`
	Instructions      string             `json:"instructions,omitempty"`
	Input             []json.RawMessage  `json:"input"`
	Tools             []functionTool     `json:"tools"`
	ParallelToolCalls bool               `json:"parallel_tool_calls"`
	Reasoning         *responseReasoning `json:"reasoning,omitempty"`
	Text              *responseText      `json:"text,omitempty"`
}

type responseText struct {
	Verbosity string `json:"verbosity"`
}

type responseReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type functionTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  agent.JSONSchema `json:"parameters"`
	Strict      *bool            `json:"strict"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type functionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type continuationState struct {
	Version int               `json:"version"`
	Items   []json.RawMessage `json:"items"`
}

func buildCreateRequest(request agent.Request, maxStateBytes int) (createResponseRequest, []json.RawMessage, error) {
	history, err := decodeState(request.State, maxStateBytes)
	if err != nil {
		return createResponseRequest{}, nil, err
	}

	newItems := encodeInputs(request.Inputs)
	input := make([]json.RawMessage, 0, len(history)+len(newItems))
	input = append(input, history...)
	input = append(input, newItems...)

	tools := make([]functionTool, len(request.Tools))
	for i, definition := range request.Tools {
		tools[i] = functionTool{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
		}
	}

	return createResponseRequest{
		Model:        request.Model,
		Instructions: request.Instructions,
		Input:        input,
		Tools:        tools,
		Store:        false,
		Stream:       false,
		Include:      []string{"reasoning.encrypted_content"},
	}, newItems, nil
}

func buildCompactRequest(request agent.Request, maxStateBytes int) (compactRequest, error) {
	createRequest, _, err := buildCreateRequest(request, maxStateBytes)
	if err != nil {
		return compactRequest{}, err
	}

	return compactRequest{
		Model:        createRequest.Model,
		Instructions: createRequest.Instructions,
		Input:        createRequest.Input,
		Tools:        createRequest.Tools,
	}, nil
}

func encodeInputs(inputs []agent.Input) []json.RawMessage {
	items := make([]json.RawMessage, len(inputs))
	for i, input := range inputs {
		var value any = inputMessage{Role: "user", Content: input.Text}
		if input.Kind == agent.InputToolResult {
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = functionCallOutput{Type: "function_call_output", CallID: input.CallID, Output: output}
		}
		items[i], _ = json.Marshal(value)
	}
	return items
}

func decodeState(encoded []byte, maxStateBytes int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	var state continuationState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != continuationStateVersion {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}

	return state.Items, nil
}

func encodeState(history, newInputs, output []json.RawMessage, maxStateBytes int) ([]byte, error) {
	items := make([]json.RawMessage, 0, len(history)+len(newInputs)+len(output))
	items = append(items, history...)
	items = append(items, newInputs...)
	items = append(items, output...)

	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: items})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	return encoded, nil
}

func validateRawObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}

	return nil
}
