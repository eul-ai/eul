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

	"yaah/agent"
)

var errUnknownTool = errors.New("tool: unknown tool")

type Tool interface {
	Definition() agent.ToolDefinition
	Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error)
}

type presenter interface {
	Presentation(agent.ToolCallSnapshot) agent.ToolPresentation
}

type Registry struct {
	definitions []agent.ToolDefinition
	tools       map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	registry := &Registry{
		definitions: make([]agent.ToolDefinition, 0, len(tools)),
		tools:       make(map[string]Tool, len(tools)),
	}

	for _, registered := range tools {
		definition := registered.Definition()
		registry.tools[definition.Name] = registered
		registry.definitions = append(registry.definitions, definition)
	}

	slices.SortFunc(registry.definitions, func(a, b agent.ToolDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})

	return registry
}

func (r *Registry) Definitions() []agent.ToolDefinition {
	return r.definitions
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
	presentation := provider.Presentation(snapshot)
	if presentation.Title == "" {
		presentation.Title = snapshot.Name
	}
	presentation.Lines = append([]string(nil), presentation.Lines...)
	return presentation
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
	var closeErrors []error
	for _, registered := range r.tools {
		closer, ok := registered.(interface{ Close() error })
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
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
