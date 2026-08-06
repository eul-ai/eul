package agent

import "context"

type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Description          string                `json:"description,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	AnyOf                []JSONSchema          `json:"anyOf,omitempty"`
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  JSONSchema
}

type ToolResult struct {
	CallID  string
	Tool    string
	Output  string
	IsError bool
}

type Toolbox interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, call ToolCall) (ToolResult, error)
}
