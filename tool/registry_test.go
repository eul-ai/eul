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

func (t fakeTool) Definition() agent.ToolDefinition { return t.definition }

func (t fakeTool) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	if t.execute == nil {
		return agent.ToolResult{}, nil
	}
	return t.execute(ctx, arguments)
}

func TestRegistryDefinitionsAreSorted(t *testing.T) {
	registry := NewRegistry(
		fakeTool{definition: agent.ToolDefinition{Name: "write"}},
		fakeTool{definition: agent.ToolDefinition{Name: "read"}},
	)
	definitions := registry.Definitions()
	if got := []string{definitions[0].Name, definitions[1].Name}; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("definition order = %v", got)
	}
}

func TestRegistryDispatchesAndCorrelatesResult(t *testing.T) {
	registry := NewRegistry(fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(_ context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
			if string(arguments) != `{"path":"README.md"}` {
				t.Fatalf("arguments = %s", arguments)
			}
			return agent.ToolResult{CallID: "wrong", Tool: "wrong", Output: "contents"}, nil
		},
	})
	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.CallID != "call-1" || result.Tool != "read" || result.Output != "contents" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRegistryCorrelatesErrorsAndRejectsUnknownTools(t *testing.T) {
	toolErr := errors.New("failed")
	registry := NewRegistry(fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			return agent.ToolResult{}, toolErr
		},
	})
	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, toolErr) || result.CallID != "call-1" || result.Tool != "read" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if _, err := registry.Execute(context.Background(), agent.ToolCall{Name: "missing"}); !errors.Is(err, errUnknownTool) {
		t.Fatalf("unknown tool error = %v", err)
	}
}

func TestDecodeArguments(t *testing.T) {
	type arguments struct {
		Path  string `json:"path"`
		Limit *int   `json:"limit"`
	}
	limit := 10
	for _, test := range []struct {
		name    string
		input   string
		want    arguments
		wantErr string
	}{
		{name: "valid", input: `{"path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "case insensitive", input: `{"PATH":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "duplicate uses last", input: `{"path":"first","path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "unknown", input: `{"path":"README.md","extra":true}`, wantErr: "unknown field"},
		{name: "non object", input: `[]`, wantErr: "JSON object"},
		{name: "null", input: `null`, wantErr: "JSON object"},
		{name: "malformed", input: `{"path":`, wantErr: "decode arguments"},
		{name: "trailing", input: `{"path":"README.md"} {}`, wantErr: "multiple JSON values"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeArguments[arguments](json.RawMessage(test.input))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got.Path != test.want.Path || got.Limit == nil || *got.Limit != *test.want.Limit {
				t.Fatalf("arguments=%+v error=%v", got, err)
			}
		})
	}
}
