package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type fakeTool struct {
	definition agent.ToolDefinition
	execute    func(context.Context, json.RawMessage) (agent.ToolResult, error)
}

func (t fakeTool) Definition() agent.ToolDefinition { return t.definition }

func (fakeTool) Presentation(string) agent.ToolPresentation {
	return agent.ToolPresentation{}
}

func (t fakeTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if t.execute == nil {
		return agent.ToolResult{}, nil
	}
	return t.execute(ctx, arguments)
}

func mustRegistry(t *testing.T, tools ...Tool) *Registry {
	t.Helper()
	registry, err := NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestRegistryDefinitionsAreSortedAndDefensive(t *testing.T) {
	registry := mustRegistry(
		t,
		fakeTool{definition: agent.ToolDefinition{Name: "write"}},
		fakeTool{definition: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Properties: map[string]agent.JSONSchema{
			"path":   {Type: "string"},
			"offset": {Type: []string{"integer", "null"}},
		}}}},
	)

	definitions := registry.Definitions()
	if got := []string{definitions[0].Name, definitions[1].Name}; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("definition order = %v", got)
	}
	definitions[0].Name = "changed"
	definitions[0].Parameters.Properties["path"] = agent.JSONSchema{Type: "number"}
	definitions[0].Parameters.Properties["offset"].Type.([]string)[0] = "number"
	fresh := registry.Definitions()
	offsetTypes, _ := fresh[0].Parameters.Properties["offset"].Type.([]string)
	if fresh[0].Name != "read" || fresh[0].Parameters.Properties["path"].Type != "string" || !slices.Equal(offsetTypes, []string{"integer", "null"}) {
		t.Fatalf("definitions share registry storage: %+v", fresh)
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []Tool
	}{
		{name: "empty", tools: []Tool{fakeTool{}}},
		{name: "duplicate", tools: []Tool{
			fakeTool{definition: agent.ToolDefinition{Name: "read"}},
			fakeTool{definition: agent.ToolDefinition{Name: "read"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.tools); err == nil {
				t.Fatal("NewRegistry succeeded")
			}
		})
	}
}

func TestRegistryPresentsPartialArgumentsLeniently(t *testing.T) {
	type arguments struct {
		Path    string `json:"path"`
		Count   int    `json:"count"`
		Content string `json:"content"`
	}
	var presented arguments
	registry := mustRegistry(t, spec[arguments]{
		definition: agent.ToolDefinition{Name: "write"},
		present: func(args arguments) agent.ToolPresentation {
			presented = args
			return agent.ToolPresentation{}
		},
	})

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		ID: "call", Name: "write", RawArguments: `{"path":"demo.go","count":"many","content":"partial`,
	})
	if presentation.Title != "write" || presented != (arguments{Path: "demo.go", Content: "partial"}) {
		t.Fatalf("presentation=%+v arguments=%+v", presentation, presented)
	}
}

func TestRegistryDispatchesAndCorrelatesResult(t *testing.T) {
	registry := mustRegistry(t, fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(_ context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
			if string(arguments) != `{"path":"README.md"}` {
				t.Fatalf("arguments = %s", arguments)
			}
			return agent.ToolResult{CallID: "wrong", Tool: "wrong", Output: "contents"}, nil
		},
	})

	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.CallID != "call-1" || result.Tool != "read" || result.Output != "contents" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRegistryCorrelatesErrorsAndReturnsUnknownToolsAsResults(t *testing.T) {
	toolErr := errors.New("failed")
	registry := mustRegistry(t, fakeTool{
		definition: agent.ToolDefinition{Name: "read"},
		execute: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			return agent.ToolResult{}, toolErr
		},
	})

	result, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}, nil)
	if !errors.Is(err, toolErr) || result.CallID != "call-1" || result.Tool != "read" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	missing := "missing-" + strings.Repeat("x", defaultMaxBytes)
	unknown, err := registry.Execute(context.Background(), agent.ToolCall{ID: "call-2", Name: missing}, nil)
	if err != nil || unknown.CallID != "call-2" || unknown.Tool != missing || !unknown.IsError || len(unknown.Output) > defaultMaxBytes {
		t.Fatalf("unknown tool result=%+v error=%v", unknown, err)
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
		wantErr bool
	}{
		{name: "valid", input: `{"path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "case insensitive", input: `{"PATH":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "duplicate uses last", input: `{"path":"first","path":"README.md","limit":10}`, want: arguments{Path: "README.md", Limit: &limit}},
		{name: "unknown", input: `{"path":"README.md","extra":true}`, wantErr: true},
		{name: "non object", input: `[]`, wantErr: true},
		{name: "malformed", input: `{"path":`, wantErr: true},
		{name: "trailing", input: `{"path":"README.md"} {}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeArguments[arguments](json.RawMessage(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatal("decodeArguments succeeded")
				}
				return
			}
			if err != nil || got.Path != test.want.Path || got.Limit == nil || *got.Limit != *test.want.Limit {
				t.Fatalf("arguments=%+v error=%v", got, err)
			}
		})
	}
}
