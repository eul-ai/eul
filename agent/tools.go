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

type ToolDiffLineKind uint8

const (
	ToolDiffLineContext ToolDiffLineKind = iota
	ToolDiffLineAdded
	ToolDiffLineRemoved
	ToolDiffLineOmitted
)

type ToolDiffLine struct {
	Kind    ToolDiffLineKind
	OldLine int
	NewLine int
	Text    string
}

type ToolPresentation struct {
	Title     string
	Arguments string
	Lines     []string
	Diff      []ToolDiffLine
	Markdown  bool
	Outcome   string
}

// ToolUpdateSink may be called concurrently before Execute returns.
type ToolUpdateSink interface {
	Update(ToolPresentation) error
	// SetFinal replaces the presentation attached to the tool-end event and ignores later updates.
	SetFinal(ToolPresentation)
}

type ToolResult struct {
	CallID  string
	Tool    string
	Output  string
	IsError bool
}

type Toolbox interface {
	Definitions() []ToolDefinition
	Presentation(ToolCallSnapshot) ToolPresentation
	Execute(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error)
}
