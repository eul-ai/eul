package responses

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

const (
	continuationStateVersion       = 1
	continuationStateEnvelopeBytes = len(`{"version":1,"items":[]}`)
)

type createResponseRequest struct {
	SessionID         string            `json:"session_id,omitempty"`
	Model             string            `json:"model"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	Instructions      string            `json:"instructions"`
	Input             []json.RawMessage `json:"input"`
	Tools             []functionTool    `json:"tools"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include,omitempty"`
	Text              *responseText     `json:"text,omitempty"`
	Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
}

type responseText struct {
	Verbosity string `json:"verbosity"`
}

type Reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type functionTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  agent.JSONSchema `json:"parameters"`
	Strict      bool             `json:"strict"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type inputContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
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
	return buildCreateRequestWithLimit(request, maxStateBytes, maxStateBytes)
}

func buildCreateRequestWithLimit(request agent.Request, maxStateBytes, generationStateBytes int) (createResponseRequest, []json.RawMessage, error) {
	created, newItems, err := buildCreateRequestUnchecked(request, maxStateBytes)
	if err != nil {
		return createResponseRequest{}, nil, err
	}
	if _, err := encodeState(created.Input[:len(created.Input)-len(newItems)], newItems, nil, generationStateBytes); err != nil {
		return createResponseRequest{}, nil, err
	}
	return created, newItems, nil
}

func buildCreateRequestUnchecked(request agent.Request, maxStateBytes int) (createResponseRequest, []json.RawMessage, error) {
	history, err := decodeState(request.State, maxStateBytes)
	if err != nil {
		return createResponseRequest{}, nil, err
	}

	newItems, err := encodeInputs(request.Inputs)
	if err != nil {
		return createResponseRequest{}, nil, err
	}
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
			Strict:      true,
		}
	}

	return createResponseRequest{
		Model:        request.Model,
		Instructions: request.Instructions,
		Input:        input,
		Tools:        tools,
		Store:        false,
		Stream:       false,
	}, newItems, nil
}

func buildCompactRequest(request agent.Request, maxStateBytes int) (createResponseRequest, error) {
	compactRequest, _, err := buildCreateRequestUnchecked(request, maxStateBytes)
	if err != nil {
		return createResponseRequest{}, err
	}

	trigger, _ := json.Marshal(struct {
		Type string `json:"type"`
	}{Type: "compaction_trigger"})
	compactRequest.Input = append(compactRequest.Input, trigger)

	return compactRequest, nil
}

func compactedStateItems(input, output []json.RawMessage) []json.RawMessage {
	items := make([]json.RawMessage, 0, len(input)+len(output))
	for _, raw := range input {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "agent_message" || item.Role == "user" || item.Role == "developer" || item.Role == "system" {
			items = append(items, raw)
		}
	}

	return append(items, output...)
}

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
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
			value = inputMessage{Role: "user", Content: input.Text}
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
	items := continuationStateItems(history, newInputs, output)

	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: items})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maxStateBytes {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maxStateBytes)
	}

	return encoded, nil
}

func continuationStateItems(groups ...[]json.RawMessage) []json.RawMessage {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	items := make([]json.RawMessage, 0, total)
	for _, group := range groups {
		items = append(items, group...)
	}
	return items
}

func encodedStateSize(groups ...[]json.RawMessage) (int, error) {
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Items: continuationStateItems(groups...)})
	if err != nil {
		return 0, fmt.Errorf("encode continuation state: %w", err)
	}
	return len(encoded), nil
}

func validateRawObject(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errors.New("must be a JSON object")
	}

	return nil
}
