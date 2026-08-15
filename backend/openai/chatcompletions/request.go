package chatcompletions

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
	continuationStateEnvelopeBytes = len(`{"version":1,"messages":[]}`)
)

type createRequest struct {
	Model             string            `json:"model"`
	Messages          []json.RawMessage `json:"messages"`
	Tools             []functionTool    `json:"tools,omitempty"`
	Stream            bool              `json:"stream"`
	StreamOptions     *streamOptions    `json:"stream_options,omitempty"`
	MaxTokens         int               `json:"max_tokens,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type functionTool struct {
	Type     string         `json:"type"`
	Function toolDefinition `json:"function"`
}

type toolDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  agent.JSONSchema `json:"parameters"`
}

type message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type assistantMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type continuationState struct {
	Version  int               `json:"version"`
	Messages []json.RawMessage `json:"messages"`
}

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
			},
		}
	}

	return createRequest{
		Model:    request.Model,
		Messages: messages,
		Tools:    tools,
	}, history, newMessages, nil
}

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	messages := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("input %d: %w", index, err)
		}

		var value message
		switch input.Kind {
		case agent.InputUser:
			value = message{Role: "user", Content: encodeUserContent(input.Content)}
		case agent.InputInbox:
			value = message{Role: "user", Content: input.Text}
		case agent.InputToolResult:
			output := input.Text
			if input.IsError {
				output = "[tool error]\n" + output
			}
			value = message{Role: "tool", Content: output, ToolCallID: input.CallID}
		}
		messages[index], _ = json.Marshal(value)
	}
	return messages, nil
}

func encodeUserContent(content []agent.ContentPart) any {
	var text string
	hasImage := false
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			text += part.Text
		case agent.ContentPartImage:
			hasImage = true
		}
	}
	if !hasImage {
		return text
	}

	parts := make([]contentPart, 0, len(content))
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			parts = append(parts, contentPart{Type: "text", Text: part.Text})
		case agent.ContentPartImage:
			if part.Image == nil {
				continue
			}
			parts = append(parts, contentPart{
				Type: "image_url",
				ImageURL: &imageURL{
					URL: "data:" + part.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Image.Data),
				},
			})
		}
	}
	return parts
}

func decodeState(encoded []byte, maximum int) ([]json.RawMessage, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}

	var state continuationState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return nil, fmt.Errorf("decode continuation state: %w", err)
	}
	if state.Version != continuationStateVersion {
		return nil, fmt.Errorf("unsupported continuation state version %d", state.Version)
	}
	for index, raw := range state.Messages {
		if err := validateRawObject(raw); err != nil {
			return nil, fmt.Errorf("continuation state message %d: %w", index, err)
		}
	}
	return state.Messages, nil
}

func encodeState(history, newMessages, output []json.RawMessage, maximum int) ([]byte, error) {
	messages := make([]json.RawMessage, 0, len(history)+len(newMessages)+len(output))
	messages = append(messages, history...)
	messages = append(messages, newMessages...)
	messages = append(messages, output...)
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("encode continuation state: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("continuation state exceeds %d bytes", maximum)
	}
	return encoded, nil
}

func encodedStateSize(groups ...[]json.RawMessage) (int, error) {
	messages := make([]json.RawMessage, 0)
	for _, group := range groups {
		messages = append(messages, group...)
	}
	encoded, err := json.Marshal(continuationState{Version: continuationStateVersion, Messages: messages})
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
