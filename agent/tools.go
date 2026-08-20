package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"
)

type JSONSchema struct {
	Type                 any                   `json:"type,omitempty"`
	Description          string                `json:"description,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	AnyOf                []JSONSchema          `json:"anyOf,omitempty"`
}

func (schema JSONSchema) MarshalJSON() ([]byte, error) {
	type wireSchema struct {
		Type                 any             `json:"type,omitempty"`
		Description          string          `json:"description,omitempty"`
		Properties           json.RawMessage `json:"properties,omitempty"`
		Required             []string        `json:"required,omitempty"`
		AdditionalProperties *bool           `json:"additionalProperties,omitempty"`
		Items                *JSONSchema     `json:"items,omitempty"`
		AnyOf                []JSONSchema    `json:"anyOf,omitempty"`
	}

	var properties json.RawMessage
	if len(schema.Properties) != 0 {
		encoded, err := marshalSchemaProperties(schema.Properties, schema.Required)
		if err != nil {
			return nil, err
		}
		properties = encoded
	}

	return json.Marshal(wireSchema{
		Type:                 schema.Type,
		Description:          schema.Description,
		Properties:           properties,
		Required:             schema.Required,
		AdditionalProperties: schema.AdditionalProperties,
		Items:                schema.Items,
		AnyOf:                schema.AnyOf,
	})
}

func marshalSchemaProperties(properties map[string]JSONSchema, required []string) ([]byte, error) {
	names := make([]string, 0, len(properties))
	seen := make(map[string]struct{}, len(properties))
	for _, name := range required {
		if _, exists := properties[name]; !exists {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		names = append(names, name)
		seen[name] = struct{}{}
	}

	remaining := make([]string, 0, len(properties)-len(names))
	for name := range properties {
		if _, exists := seen[name]; !exists {
			remaining = append(remaining, name)
		}
	}
	slices.Sort(remaining)
	names = append(names, remaining...)

	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, name := range names {
		if index != 0 {
			encoded.WriteByte(',')
		}
		encodedName, _ := json.Marshal(name)
		encodedProperty, err := json.Marshal(properties[name])
		if err != nil {
			return nil, err
		}
		encoded.Write(encodedName)
		encoded.WriteByte(':')
		encoded.Write(encodedProperty)
	}
	encoded.WriteByte('}')
	return encoded.Bytes(), nil
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
	// Execute may be called concurrently for calls from the same provider response.
	Execute(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error)
}

type ToolPresenter interface {
	Presentation(ToolCallSnapshot) ToolPresentation
}
