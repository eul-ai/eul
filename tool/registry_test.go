package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"yaah/agent"
)

type fakeTool struct {
	definition agent.ToolDefinition
	execute    func(context.Context, json.RawMessage) (agent.ToolResult, error)
}

func (t fakeTool) Definition() agent.ToolDefinition {
	return t.definition
}

func (t fakeTool) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	if t.execute == nil {
		return agent.ToolResult{}, nil
	}
	return t.execute(ctx, arguments)
}

func TestRegistryDefinitionsAreSortedAndCopied(t *testing.T) {
	additionalProperties := false
	registry, err := NewRegistry(
		fakeTool{definition: agent.ToolDefinition{Name: "write", PromptGuidelines: []string{"write guidance"}}},
		fakeTool{definition: agent.ToolDefinition{
			Name: "read",
			Parameters: agent.JSONSchema{
				Type:                 "object",
				AdditionalProperties: &additionalProperties,
				Properties:           map[string]agent.JSONSchema{"path": {Type: "string"}},
			},
		}},
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	definitions := registry.Definitions()
	if got := []string{definitions[0].Name, definitions[1].Name}; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("definition order = %v", got)
	}

	definitions[0].Name = "changed"
	if fresh := registry.Definitions(); fresh[0].Name != "read" {
		t.Fatalf("registry definitions were mutated through returned slice: %+v", fresh)
	}
}

func TestRegistryRejectsInvalidAndDuplicateTools(t *testing.T) {
	tests := []struct {
		name  string
		tools []Tool
		want  string
	}{
		{name: "nil", tools: []Tool{nil}, want: "nil tool"},
		{name: "typed nil", tools: []Tool{(*fakeTool)(nil)}, want: "nil tool"},
		{name: "empty name", tools: []Tool{fakeTool{}}, want: "tool name is required"},
		{
			name: "duplicate",
			tools: []Tool{
				fakeTool{definition: agent.ToolDefinition{Name: "read"}},
				fakeTool{definition: agent.ToolDefinition{Name: "read"}},
			},
			want: "duplicate tool",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRegistry(test.tools...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRegistry() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRegistryDispatchesAndCorrelatesResult(t *testing.T) {
	registry, err := NewRegistry(fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(_ context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
			if string(arguments) != `{"path":"README.md"}` {
				t.Fatalf("arguments = %s", arguments)
			}
			return agent.ToolResult{CallID: "wrong", Tool: "wrong", Output: "contents"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.CallID != "call-1" || result.Tool != "read" || result.Output != "contents" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRegistryCorrelatesResultWhenToolReturnsError(t *testing.T) {
	toolErr := errors.New("failed")
	registry, err := NewRegistry(fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			return agent.ToolResult{CallID: "wrong", Tool: "wrong"}, toolErr
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, toolErr) {
		t.Fatalf("Execute() error = %v, want tool error", err)
	}
	if result.CallID != "call-1" || result.Tool != "read" {
		t.Fatalf("error result correlation = %+v", result)
	}
}

func TestRegistryReturnsUnknownToolError(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	_, err = registry.Execute(context.Background(), agent.ToolCall{Name: "missing"})
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("Execute() error = %v, want ErrUnknownTool", err)
	}
}

func TestDecodeArgumentsIsStrict(t *testing.T) {
	type arguments struct {
		Path  string `json:"path"`
		Limit *int   `json:"limit"`
	}

	limit := 10
	tests := []struct {
		name    string
		input   string
		want    arguments
		wantErr string
	}{
		{name: "valid", input: `{"path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "unknown field", input: `{"path":"README.md","extra":true}`, wantErr: "unknown argument field"},
		{name: "case mismatch", input: `{"PATH":"README.md"}`, wantErr: "unknown argument field"},
		{name: "duplicate field", input: `{"path":"first","path":"second"}`, wantErr: "duplicate argument field"},
		{name: "non object", input: `[]`, wantErr: "JSON object"},
		{name: "null", input: `null`, wantErr: "JSON object"},
		{name: "malformed", input: `{"path":`, wantErr: "decode argument"},
		{name: "trailing", input: `{"path":"README.md"} {}`, wantErr: "multiple JSON values"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeArguments[arguments](json.RawMessage(test.input), "path", "limit")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("decodeArguments() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeArguments() error = %v", err)
			}
			if got.Path != test.want.Path || got.Limit == nil || *got.Limit != *test.want.Limit {
				t.Fatalf("decodeArguments() = %+v, want %+v", got, test.want)
			}
		})
	}
}
