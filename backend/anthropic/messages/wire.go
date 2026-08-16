package messages

import (
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
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

func marshalWireMessage(role string, blocks []contentBlock) (json.RawMessage, error) {
	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("encode %s message content: %w", role, err)
	}
	message, err := json.Marshal(wireMessage{Role: role, Content: content})
	if err != nil {
		return nil, fmt.Errorf("encode %s message: %w", role, err)
	}
	return message, nil
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
