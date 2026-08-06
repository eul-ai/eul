package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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

// NewRegistry validates tools and returns a registry sorted by tool name.
func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{
		definitions: make([]agent.ToolDefinition, 0, len(tools)),
		tools:       make(map[string]Tool, len(tools)),
	}

	for _, registered := range tools {
		if isNil(registered) {
			return nil, errors.New("tool: nil tool")
		}
		definition := registered.Definition()
		if strings.TrimSpace(definition.Name) == "" {
			return nil, errors.New("tool: tool name is required")
		}
		if _, exists := registry.tools[definition.Name]; exists {
			return nil, fmt.Errorf("tool: duplicate tool %q", definition.Name)
		}
		registry.tools[definition.Name] = registered
		registry.definitions = append(registry.definitions, definition)
	}

	slices.SortFunc(registry.definitions, func(a, b agent.ToolDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return registry, nil
}

// Definitions returns definitions sorted by tool name.
func (r *Registry) Definitions() []agent.ToolDefinition {
	if r == nil {
		return nil
	}
	return slices.Clone(r.definitions)
}

// Execute dispatches a call to the registered tool with the exact name.
func (r *Registry) Execute(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	if r == nil {
		return agent.ToolResult{}, fmt.Errorf("%w %q", ErrUnknownTool, call.Name)
	}
	registered, exists := r.tools[call.Name]
	if !exists {
		return agent.ToolResult{}, fmt.Errorf("%w %q", ErrUnknownTool, call.Name)
	}

	result, err := registered.Execute(ctx, call.Arguments)
	result.CallID = call.ID
	result.Tool = call.Name
	return result, err
}

// decodeArguments strictly decodes one JSON object into a tool-specific Go
// struct. Property names must exactly match allowedFields; duplicate,
// unknown, non-object, and trailing values are rejected.
func decodeArguments[T any](arguments json.RawMessage, allowedFields ...string) (T, error) {
	var value T
	arguments = bytes.TrimSpace(arguments)
	if len(arguments) == 0 || arguments[0] != '{' {
		return value, errors.New("tool: arguments must be a JSON object")
	}
	if err := validateArgumentKeys(arguments, allowedFields); err != nil {
		return value, err
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

func validateArgumentKeys(arguments []byte, allowedFields []string) error {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}

	decoder := json.NewDecoder(bytes.NewReader(arguments))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("tool: decode arguments: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return errors.New("tool: arguments must be a JSON object")
	}

	seen := make(map[string]struct{}, len(allowedFields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("tool: decode arguments: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("tool: argument property name must be a string")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("tool: duplicate argument field %q", key)
		}
		seen[key] = struct{}{}
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf("tool: unknown argument field %q", key)
		}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("tool: decode argument %q: %w", key, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("tool: decode arguments: %w", err)
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
