package agent

import "context"

// JSONSchema is the subset of JSON Schema needed to describe tool inputs.
// It can be extended as concrete tools require more schema features.
type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Description          string                `json:"description,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	AnyOf                []JSONSchema          `json:"anyOf,omitempty"`
}

// ToolDefinition describes a tool to both the model and the system-prompt
// builder. Parameters is serialized by a provider adapter.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  JSONSchema
}

// ToolResult is the normalized result of one tool call.
type ToolResult struct {
	CallID  string
	Tool    string
	Output  string
	IsError bool
}

// Toolbox is the tool collection consumed by the agent engine.
type Toolbox interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, call ToolCall) (ToolResult, error)
}
