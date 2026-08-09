package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
)

type fakeTool struct {
	definition agent.ToolDefinition
	execute    func(context.Context, json.RawMessage) (agent.ToolResult, error)
}

func (t fakeTool) Definition() agent.ToolDefinition { return t.definition }

func (t fakeTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if t.execute == nil {
		return agent.ToolResult{}, nil
	}
	return t.execute(ctx, arguments)
}

type presentingTool struct {
	fakeTool
	present func(PresentationSnapshot) agent.ToolPresentation
}

func (t presentingTool) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return t.present(snapshot)
}

type closeTool struct {
	fakeTool
	close func() error
}

func (t closeTool) Close() error {
	return t.close()
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
		fakeTool{definition: agent.ToolDefinition{Name: "read", Parameters: agent.JSONSchema{Properties: map[string]agent.JSONSchema{"path": {Type: "string"}}}}},
	)

	definitions := registry.Definitions()
	if got := []string{definitions[0].Name, definitions[1].Name}; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("definition order = %v", got)
	}
	definitions[0].Name = "changed"
	definitions[0].Parameters.Properties["path"] = agent.JSONSchema{Type: "number"}
	fresh := registry.Definitions()
	if fresh[0].Name != "read" || fresh[0].Parameters.Properties["path"].Type != "string" {
		t.Fatalf("definitions share registry storage: %+v", fresh)
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []Tool
		want  string
	}{
		{name: "empty", tools: []Tool{fakeTool{}}, want: "no name"},
		{name: "duplicate", tools: []Tool{
			fakeTool{definition: agent.ToolDefinition{Name: "read"}},
			fakeTool{definition: agent.ToolDefinition{Name: "read"}},
		}, want: "duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRegistry(test.tools); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRegistryParsesStreamingArgumentsForPresentation(t *testing.T) {
	var presented PresentationSnapshot
	registry := mustRegistry(t, presentingTool{
		fakeTool: fakeTool{definition: agent.ToolDefinition{Name: "write"}},
		present: func(snapshot PresentationSnapshot) agent.ToolPresentation {
			presented = snapshot
			return agent.ToolPresentation{Title: "write"}
		},
	})

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		ID: "call", Name: "write", RawArguments: `{"path":"demo.go","content":"partial`,
	})
	if presentation.Title != "write" || presented.ID != "call" || presented.Arguments["path"] != "demo.go" || presented.Arguments["content"] != "partial" {
		t.Fatalf("presentation=%+v snapshot=%+v", presentation, presented)
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

func TestRegistryCloseIsReverseOrderedIdempotentAndConcurrentSafe(t *testing.T) {
	closeErr := errors.New("close failed")
	var mu sync.Mutex
	var closed []string
	newCloser := func(name string, err error) closeTool {
		return closeTool{fakeTool: fakeTool{definition: agent.ToolDefinition{Name: name}}, close: func() error {
			mu.Lock()
			defer mu.Unlock()
			closed = append(closed, name)
			return err
		}}
	}
	registry := mustRegistry(t, newCloser("one", nil), newCloser("two", closeErr))

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 4)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- registry.Close()
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if !errors.Is(err, closeErr) {
			t.Fatalf("Close() error = %v", err)
		}
	}
	if !slices.Equal(closed, []string{"two", "one"}) {
		t.Fatalf("close order = %v", closed)
	}
}

func TestRegistryCorrelatesErrorsAndRejectsUnknownTools(t *testing.T) {
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
	if _, err := registry.Execute(context.Background(), agent.ToolCall{Name: "missing"}, nil); !errors.Is(err, errUnknownTool) {
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
		{name: "non object", input: `[]`, wantErr: "decode arguments"},
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
