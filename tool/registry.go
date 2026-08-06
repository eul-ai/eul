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

var ErrUnknownTool = errors.New("tool: unknown tool")

// Tool is the interface consumed by Registry. Concrete tool implementations
// decode their own arguments into tool-specific Go structs.
type Tool interface {
	Definition() agent.ToolDefinition
	Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error)
}

// Registry is a deterministic collection of tools and implements
// agent.Toolbox.
type Registry struct {
	definitions []agent.ToolDefinition
	tools       map[string]Tool
}

// NewRegistry returns a registry sorted by tool name.
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

// Definitions returns definitions sorted by tool name.
func (r *Registry) Definitions() []agent.ToolDefinition {
	return r.definitions
}

// Execute dispatches a call to the registered tool with the exact name.
func (r *Registry) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	registered, exists := r.tools[call.Name]
	if !exists {
		return agent.ToolResult{}, fmt.Errorf("%w %q", ErrUnknownTool, call.Name)
	}

	result, err := registered.Execute(ctx, call.Arguments)
	result.CallID = call.ID
	result.Tool = call.Name
	return result, err
}

// decodeArguments decodes one JSON object into a tool-specific Go struct.
func decodeArguments[T any](arguments json.RawMessage) (T, error) {
	var value T
	arguments = bytes.TrimSpace(arguments)
	if len(arguments) == 0 || arguments[0] != '{' {
		return value, errors.New("tool: arguments must be a JSON object")
	}
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
