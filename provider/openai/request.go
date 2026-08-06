package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"yaah/agent"
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

type responseText struct {
	Verbosity string `json:"verbosity"`
}

type responseReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
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
	if strings.TrimSpace(request.Model) == "" {
		return createResponseRequest{}, nil, errors.New("model is required")
	}

	history, err := decodeState(request.State, maxStateBytes)
	if err != nil {
		return createResponseRequest{}, nil, err
	}
	if err := validateInputCorrelation(history, request.Inputs); err != nil {
		return createResponseRequest{}, nil, err
	}
	newItems, err := encodeInputs(request.Inputs)
	if err != nil {
		return createResponseRequest{}, nil, err
	}
	input := make([]json.RawMessage, 0, len(history)+len(newItems))
	input = appendRawMessages(input, history...)
	input = appendRawMessages(input, newItems...)

	tools := make([]functionTool, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for i, definition := range request.Tools {
		if strings.TrimSpace(definition.Name) == "" {
			return createResponseRequest{}, nil, fmt.Errorf("tool %d has no name", i)
		}
		if !validToolName(definition.Name) {
			return createResponseRequest{}, nil, fmt.Errorf("tool name %q must contain 1-64 letters, digits, underscores, or hyphens", definition.Name)
		}
		if _, exists := toolNames[definition.Name]; exists {
			return createResponseRequest{}, nil, fmt.Errorf("duplicate tool name %q", definition.Name)
		}
		toolNames[definition.Name] = struct{}{}
		if definition.Parameters.Type != "object" {
			return createResponseRequest{}, nil, fmt.Errorf("tool %q parameters must have type object", definition.Name)
		}
		if err := validateStrictSchema(definition.Parameters, "parameters"); err != nil {
			return createResponseRequest{}, nil, fmt.Errorf("tool %q: %w", definition.Name, err)
		}
		strict := true
		tools[i] = functionTool{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  definition.Parameters,
			Strict:      &strict,
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

func validateInputCorrelation(history []json.RawMessage, inputs []agent.Input) error {
	pending := make(map[string]string)
	seenCalls := make(map[string]struct{})
	seenOutputs := make(map[string]struct{})
	for i, raw := range history {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("decode continuation item %d: %w", i, err)
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return fmt.Errorf("continuation function call %d is missing correlation fields", i)
			}
			if _, exists := seenCalls[item.CallID]; exists {
				return fmt.Errorf("duplicate continuation function call ID %q", item.CallID)
			}
			seenCalls[item.CallID] = struct{}{}
			pending[item.CallID] = item.Name
		case "function_call_output":
			if item.CallID == "" {
				return fmt.Errorf("continuation tool output %d has no call ID", i)
			}
			if _, exists := seenOutputs[item.CallID]; exists {
				return fmt.Errorf("duplicate continuation tool output for call ID %q", item.CallID)
			}
			if _, exists := pending[item.CallID]; !exists {
				return fmt.Errorf("continuation tool output has unknown call ID %q", item.CallID)
			}
			seenOutputs[item.CallID] = struct{}{}
			delete(pending, item.CallID)
		}
	}

	for i, input := range inputs {
		switch input.Kind {
		case agent.InputUser:
			if len(pending) != 0 {
				return fmt.Errorf("user input %d follows unresolved function calls", i)
			}
		case agent.InputToolResult:
			name, exists := pending[input.CallID]
			if !exists {
				return fmt.Errorf("tool result input %d has unknown call ID %q", i, input.CallID)
			}
			if input.Tool == "" {
				return fmt.Errorf("tool result input %d has no tool name", i)
			}
			if input.Tool != name {
				return fmt.Errorf("tool result input %d names %q, want %q", i, input.Tool, name)
			}
			delete(pending, input.CallID)
		}
	}
	if len(pending) != 0 {
		return errors.New("tool results do not resolve every pending function call")
	}
	return nil
}

func validateStrictSchema(schema agent.JSONSchema, location string) error {
	return validateStrictSchemaAt(schema, location, 0)
}

func validateStrictSchemaAt(schema agent.JSONSchema, location string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("%s exceeds maximum schema depth", location)
	}
	if schema.Type == "object" || len(schema.Properties) != 0 {
		if schema.Type != "object" {
			return fmt.Errorf("%s with properties must have type object", location)
		}
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			return fmt.Errorf("%s must set additionalProperties to false", location)
		}
		required := make(map[string]struct{}, len(schema.Required))
		for _, name := range schema.Required {
			if _, exists := required[name]; exists {
				return fmt.Errorf("%s has duplicate required property %q", location, name)
			}
			if _, exists := schema.Properties[name]; !exists {
				return fmt.Errorf("%s requires unknown property %q", location, name)
			}
			required[name] = struct{}{}
		}
		for name, property := range schema.Properties {
			if _, exists := required[name]; !exists {
				return fmt.Errorf("%s property %q must be required in strict mode", location, name)
			}
			if err := validateStrictSchemaAt(property, location+"."+name, depth+1); err != nil {
				return err
			}
		}
	}
	if schema.Items != nil {
		if err := validateStrictSchemaAt(*schema.Items, location+"[]", depth+1); err != nil {
			return err
		}
	}
	for i, alternative := range schema.AnyOf {
		if err := validateStrictSchemaAt(alternative, fmt.Sprintf("%s.anyOf[%d]", location, i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(inputs))
	for i, input := range inputs {
		var value any
		switch input.Kind {
		case agent.InputUser:
			value = inputMessage{Role: "user", Content: input.Text}
		case agent.InputToolResult:
			if input.CallID == "" {
				return nil, fmt.Errorf("tool result input %d has no call ID", i)
			}
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = functionCallOutput{Type: "function_call_output", CallID: input.CallID, Output: output}
		default:
			return nil, fmt.Errorf("input %d has unsupported kind %q", i, input.Kind)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode input %d: %w", i, err)
		}
		items = append(items, encoded)
	}
	return items, nil
}

func decodeState(encoded []byte, maxStateBytes int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var state continuationState
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode continuation state: multiple JSON values")
		}
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != continuationStateVersion {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}
	if state.Items == nil {
		return nil, errors.New("continuation state items must be an array")
	}
	for i, item := range state.Items {
		if err := validateRawObject(item); err != nil {
			return nil, fmt.Errorf("continuation state item %d: %w", i, err)
		}
	}
	return appendRawMessages(nil, state.Items...), nil
}

func encodeState(history, newInputs, output []json.RawMessage, maxStateBytes int) ([]byte, error) {
	items := make([]json.RawMessage, 0, len(history)+len(newInputs)+len(output))
	items = appendRawMessages(items, history...)
	items = appendRawMessages(items, newInputs...)
	items = appendRawMessages(items, output...)
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: items})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}
	return encoded, nil
}

func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateRawObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}
	return nil
}

func appendRawMessages(destination []json.RawMessage, values ...json.RawMessage) []json.RawMessage {
	for _, value := range values {
		destination = append(destination, append(json.RawMessage(nil), value...))
	}
	return destination
}
