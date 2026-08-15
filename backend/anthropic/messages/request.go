package messages

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
	Model        string            `json:"model"`
	System       []systemBlock     `json:"system,omitempty"`
	Messages     []json.RawMessage `json:"messages"`
	Tools        []toolDefinition  `json:"tools,omitempty"`
	ToolChoice   *ToolChoice       `json:"tool_choice,omitempty"`
	MaxTokens    int               `json:"max_tokens"`
	Stream       bool              `json:"stream"`
	Thinking     *Thinking         `json:"thinking,omitempty"`
	OutputConfig *OutputConfig     `json:"output_config,omitempty"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type toolDefinition struct {
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	InputSchema  agent.JSONSchema `json:"input_schema"`
	CacheControl *cacheControl    `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type OutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type contentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Source       *imageSource    `json:"source,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
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

func withPromptCacheControl(request createRequest) (createRequest, error) {
	request.Tools = append([]toolDefinition(nil), request.Tools...)
	request.System = append([]systemBlock(nil), request.System...)
	request.Messages = append([]json.RawMessage(nil), request.Messages...)

	if len(request.Tools) != 0 {
		request.Tools[len(request.Tools)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if len(request.System) != 0 {
		request.System[len(request.System)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}

	start := max(0, len(request.Messages)-2)
	for index := start; index < len(request.Messages); index++ {
		var message wireMessage
		if err := json.Unmarshal(request.Messages[index], &message); err != nil {
			return createRequest{}, fmt.Errorf("decode message %d for prompt caching: %w", index, err)
		}

		var blocks []contentBlock
		if err := json.Unmarshal(message.Content, &blocks); err != nil {
			return createRequest{}, fmt.Errorf("decode message %d content for prompt caching: %w", index, err)
		}
		if len(blocks) == 0 {
			continue
		}
		blocks[len(blocks)-1].CacheControl = &cacheControl{Type: "ephemeral"}

		content, err := json.Marshal(blocks)
		if err != nil {
			return createRequest{}, fmt.Errorf("encode message %d content for prompt caching: %w", index, err)
		}
		message.Content = content
		request.Messages[index], err = json.Marshal(message)
		if err != nil {
			return createRequest{}, fmt.Errorf("encode message %d for prompt caching: %w", index, err)
		}
	}

	return request, nil
}

func encodeInputs(inputs []agent.Input) ([]json.RawMessage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	blocks := make([]contentBlock, 0, len(inputs))
	for index, input := range inputs {
		if err := input.Validate(); err != nil {
			return nil, fmt.Errorf("input %d: %w", index, err)
		}

		switch input.Kind {
		case agent.InputUser:
			for _, part := range input.Content {
				switch part.Kind {
				case agent.ContentPartText:
					if part.Text != "" {
						blocks = append(blocks, contentBlock{Type: "text", Text: part.Text})
					}
				case agent.ContentPartImage:
					if part.Image == nil {
						continue
					}
					blocks = append(blocks, contentBlock{
						Type: "image",
						Source: &imageSource{
							Type:      "base64",
							MediaType: part.Image.MediaType,
							Data:      base64.StdEncoding.EncodeToString(part.Image.Data),
						},
					})
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
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("inputs contain no Anthropic content blocks")
	}

	content, _ := json.Marshal(blocks)
	message, _ := json.Marshal(wireMessage{Role: "user", Content: content})
	return []json.RawMessage{message}, nil
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
