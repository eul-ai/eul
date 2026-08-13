package agent

import (
	"context"
	"slices"
	"time"
)

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

// Values are persisted in terminal checkpoints and must remain stable.
const (
	ToolDiffLineContext ToolDiffLineKind = 0
	ToolDiffLineAdded   ToolDiffLineKind = 1
	ToolDiffLineRemoved ToolDiffLineKind = 2
	ToolDiffLineOmitted ToolDiffLineKind = 3
)

type ToolDiffLine struct {
	Kind    ToolDiffLineKind
	OldLine int
	NewLine int
	Text    string
}

type ToolPresentation struct {
	Title          string
	Arguments      string
	Lines          []string
	LinesTruncated bool
	Diff           []ToolDiffLine
	Markdown       bool
	Outcome        string
	TailLines      int
	Elapsed        time.Duration
	Timeout        time.Duration
}

func (presentation ToolPresentation) Clone() ToolPresentation {
	presentation.Lines = slices.Clone(presentation.Lines)
	presentation.Diff = slices.Clone(presentation.Diff)
	return presentation
}

func (presentation ToolPresentation) Equal(other ToolPresentation) bool {
	return presentation.Title == other.Title &&
		presentation.Arguments == other.Arguments &&
		presentation.LinesTruncated == other.LinesTruncated &&
		presentation.Markdown == other.Markdown &&
		presentation.Outcome == other.Outcome &&
		presentation.TailLines == other.TailLines &&
		presentation.Elapsed == other.Elapsed &&
		presentation.Timeout == other.Timeout &&
		slices.Equal(presentation.Lines, other.Lines) &&
		slices.Equal(presentation.Diff, other.Diff)
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
	// Execute may be called concurrently for calls from the same provider response.
	Execute(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error)
}
