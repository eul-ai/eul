package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/eul-ai/eul/agent"
)

var errUnknownTool = errors.New("tool: unknown tool")

// Tool returns ordinary argument and execution failures as ToolResult values with
// IsError set. It returns a Go error only when the agent turn itself must stop,
// such as cancellation or a failed presentation update. Execute may be called
// concurrently for calls from the same provider response.
type Tool interface {
	Definition() agent.ToolDefinition
	Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error)
}

type PresentationSnapshot struct {
	ID           string
	Name         string
	RawArguments string
	Arguments    map[string]any
	Complete     bool
}

type presenter interface {
	Presentation(PresentationSnapshot) agent.ToolPresentation
}

type Registry struct {
	definitions []agent.ToolDefinition
	tools       map[string]Tool
	ordered     []Tool
	resources   []io.Closer
	closeOnce   sync.Once
	closeErr    error
}

func NewRegistry(tools []Tool, resources ...io.Closer) (*Registry, error) {
	registry := &Registry{
		definitions: make([]agent.ToolDefinition, 0, len(tools)),
		tools:       make(map[string]Tool, len(tools)),
		ordered:     append([]Tool(nil), tools...),
		resources:   append([]io.Closer(nil), resources...),
	}

	for _, registered := range tools {
		definition := registered.Definition()
		if strings.TrimSpace(definition.Name) == "" {
			return nil, errors.New("tool: registered tool has no name")
		}
		if _, exists := registry.tools[definition.Name]; exists {
			return nil, fmt.Errorf("tool: duplicate tool %q", definition.Name)
		}
		registry.tools[definition.Name] = registered
		registry.definitions = append(registry.definitions, cloneToolDefinition(definition))
	}

	slices.SortFunc(registry.definitions, func(a, b agent.ToolDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})

	return registry, nil
}

func (r *Registry) Definitions() []agent.ToolDefinition {
	definitions := make([]agent.ToolDefinition, len(r.definitions))
	for index, definition := range r.definitions {
		definitions[index] = cloneToolDefinition(definition)
	}
	return definitions
}

func (r *Registry) Presentation(snapshot agent.ToolCallSnapshot) agent.ToolPresentation {
	registered, exists := r.tools[snapshot.Name]
	if !exists {
		return agent.ToolPresentation{Title: snapshot.Name}
	}

	provider, ok := registered.(presenter)
	if !ok {
		return agent.ToolPresentation{Title: snapshot.Name}
	}
	presentation := provider.Presentation(PresentationSnapshot{
		ID:           snapshot.ID,
		Name:         snapshot.Name,
		RawArguments: snapshot.RawArguments,
		Arguments:    parseStreamingJSONObject(snapshot.RawArguments),
		Complete:     snapshot.Complete,
	})
	if presentation.Title == "" {
		presentation.Title = snapshot.Name
	}
	return presentation.Clone()
}

func (r *Registry) Execute(ctx context.Context, call agent.ToolCall, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	registered, exists := r.tools[call.Name]
	if !exists {
		return agent.ToolResult{}, fmt.Errorf("%w %q", errUnknownTool, call.Name)
	}

	result, err := registered.Execute(ctx, call.Arguments, updates)
	result.CallID = call.ID
	result.Tool = call.Name
	return result, err
}

func (r *Registry) Close() error {
	r.closeOnce.Do(func() {
		var closeErrors []error
		for index := len(r.ordered) - 1; index >= 0; index-- {
			closer, ok := r.ordered[index].(io.Closer)
			if !ok {
				continue
			}
			if err := closer.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		for index := len(r.resources) - 1; index >= 0; index-- {
			if err := r.resources[index].Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		r.closeErr = errors.Join(closeErrors...)
	})
	return r.closeErr
}

func cloneToolDefinition(definition agent.ToolDefinition) agent.ToolDefinition {
	definition.Parameters = cloneJSONSchema(definition.Parameters)
	return definition
}

func cloneJSONSchema(schema agent.JSONSchema) agent.JSONSchema {
	if schema.Properties != nil {
		properties := make(map[string]agent.JSONSchema, len(schema.Properties))
		for name, property := range schema.Properties {
			properties[name] = cloneJSONSchema(property)
		}
		schema.Properties = properties
	}
	schema.Required = slices.Clone(schema.Required)
	if schema.AdditionalProperties != nil {
		value := *schema.AdditionalProperties
		schema.AdditionalProperties = &value
	}
	if schema.Items != nil {
		items := cloneJSONSchema(*schema.Items)
		schema.Items = &items
	}
	if schema.AnyOf != nil {
		schema.AnyOf = make([]agent.JSONSchema, len(schema.AnyOf))
		for index, item := range schema.AnyOf {
			schema.AnyOf[index] = cloneJSONSchema(item)
		}
	}
	return schema
}

func decodeArguments[T any](arguments json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("tool: decode arguments: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("tool: arguments contain multiple JSON values")
		}
		return value, fmt.Errorf("tool: decode trailing arguments: %w", err)
	}

	return value, nil
}
