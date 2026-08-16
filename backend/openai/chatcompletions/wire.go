package chatcompletions

import (
	"encoding/json"

	"github.com/eul-ai/eul/agent"
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

	serializeReasoningContent bool
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
	Strict      bool             `json:"strict"`
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
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
}

func reasoningContent(value string, serializeEmpty bool) *string {
	if value == "" && !serializeEmpty {
		return nil
	}
	return &value
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
