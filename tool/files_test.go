package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

type toolUpdateSinkFunc func(agent.ToolPresentation) error

func (update toolUpdateSinkFunc) Update(presentation agent.ToolPresentation) error {
	return update(presentation)
}

func (update toolUpdateSinkFunc) SetFinal(presentation agent.ToolPresentation) {
	_ = update(presentation)
}

func TestCoreToolDefinitionsUseStrictSchemas(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	replaceTool := NewReplace(cwd)
	insertBeforeTool := NewInsertBefore(cwd)
	insertAfterTool := NewInsertAfter(cwd)
	bashTool := NewBash(cwd)

	tests := []struct {
		tool       Tool
		required   []string
		properties map[string][]string
	}{
		{tool: readTool, required: []string{"path", "offset", "limit"}, properties: map[string][]string{"path": {"string"}, "offset": {"integer", "null"}, "limit": {"integer", "null"}}},
		{tool: writeTool, required: []string{"path", "content"}, properties: map[string][]string{"path": {"string"}, "content": {"string"}}},
		{tool: replaceTool, required: []string{"path", "oldText", "newText", "all"}, properties: map[string][]string{"path": {"string"}, "oldText": {"string"}, "newText": {"string"}, "all": {"boolean"}}},
		{tool: insertBeforeTool, required: []string{"path", "anchor", "content"}, properties: map[string][]string{"path": {"string"}, "anchor": {"string"}, "content": {"string"}}},
		{tool: insertAfterTool, required: []string{"path", "anchor", "content"}, properties: map[string][]string{"path": {"string"}, "anchor": {"string"}, "content": {"string"}}},
		{tool: bashTool, required: []string{"command", "timeout", "network"}, properties: map[string][]string{"command": {"string"}, "timeout": {"integer", "null"}, "network": {"boolean"}}},
	}
	for _, test := range tests {
		definition := test.tool.Definition()
		t.Run(definition.Name, func(t *testing.T) {
			if definition.Parameters.Type != "object" {
				t.Fatalf("schema type = %q", definition.Parameters.Type)
			}
			if definition.Parameters.AdditionalProperties == nil || *definition.Parameters.AdditionalProperties {
				t.Fatal("schema does not reject additional properties")
			}
			if !slices.Equal(definition.Parameters.Required, test.required) {
				t.Fatalf("required fields = %v, want %v", definition.Parameters.Required, test.required)
			}
			if len(definition.Parameters.Properties) != len(test.properties) {
				t.Fatalf("properties = %v, want %v", definition.Parameters.Properties, test.properties)
			}
			for name, wantTypes := range test.properties {
				property, exists := definition.Parameters.Properties[name]
				if !exists {
					t.Fatalf("missing property %q", name)
				}
				var gotTypes []string
				switch schemaType := property.Type.(type) {
				case string:
					gotTypes = []string{schemaType}
				case []string:
					gotTypes = schemaType
				}
				if !slices.Equal(gotTypes, wantTypes) {
					t.Fatalf("property %q types = %v, want %v", name, gotTypes, wantTypes)
				}
			}
		})
	}
}

func TestNullableSchemaMarshalsAsTypeUnion(t *testing.T) {
	encoded, err := json.Marshal(nullable("integer", "optional integer"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type  []string        `json:"type"`
		AnyOf json.RawMessage `json:"anyOf"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Type, []string{"integer", "null"}) || len(schema.AnyOf) != 0 {
		t.Fatalf("nullable schema = %s", encoded)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestFileToolPresentationsSeparateTitleAndArguments(t *testing.T) {
	snapshot := PresentationSnapshot{Arguments: map[string]any{"path": "demo.go"}}
	presentations := []agent.ToolPresentation{
		NewRead(t.TempDir()).Presentation(snapshot),
		NewWrite(t.TempDir()).Presentation(snapshot),
		NewReplace(t.TempDir()).Presentation(snapshot),
		NewInsertBefore(t.TempDir()).Presentation(snapshot),
		NewInsertAfter(t.TempDir()).Presentation(snapshot),
		bashPresentation("go test ./...", defaultBashTimeout),
	}
	wantTitles := []string{"read", "write", "replace", "insert_before", "insert_after", "bash"}
	wantArguments := []string{"demo.go", "demo.go", "demo.go", "demo.go", "demo.go", `"go test ./..."`}
	for index, presentation := range presentations {
		if presentation.Title != wantTitles[index] || presentation.Arguments != wantArguments[index] {
			t.Fatalf("presentation %d = %+v", index, presentation)
		}
	}
}

func TestBashPresentationShowsTimeout(t *testing.T) {
	bashTool := NewBash(t.TempDir())

	defaultPresentation := bashTool.Presentation(PresentationSnapshot{Arguments: map[string]any{"command": "sleep 1", "timeout": nil}})
	if defaultPresentation.Timeout != defaultBashTimeout {
		t.Fatalf("default presentation = %+v", defaultPresentation)
	}

	customPresentation := bashTool.Presentation(PresentationSnapshot{Arguments: map[string]any{"command": "sleep 1", "timeout": json.Number("30")}})
	if customPresentation.Timeout != 30*time.Second {
		t.Fatalf("custom presentation = %+v", customPresentation)
	}
}

func TestFilesystemToolsHonorPreCanceledContext(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	replaceTool := NewReplace(cwd)
	insertBeforeTool := NewInsertBefore(cwd)
	insertAfterTool := NewInsertAfter(cwd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := []struct {
		tool Tool
		args string
	}{
		{readTool, `{"path":"missing"}`},
		{writeTool, `{"path":"file","content":"content"}`},
		{replaceTool, `{"path":"file","oldText":"old","newText":"new","all":false}`},
		{insertBeforeTool, `{"path":"file","anchor":"","content":"new"}`},
		{insertAfterTool, `{"path":"file","anchor":"","content":"new"}`},
	}
	for _, call := range calls {
		_, err := call.tool.Execute(ctx, json.RawMessage(call.args), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation error = %v", call.tool.Definition().Name, err)
		}
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Fatalf("canceled filesystem tools changed cwd: entries=%v err=%v", entries, err)
	}
}

func TestCoreToolsRegisterInDeterministicOrder(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	replaceTool := NewReplace(cwd)
	insertBeforeTool := NewInsertBefore(cwd)
	insertAfterTool := NewInsertAfter(cwd)
	bashTool := NewBash(cwd)

	registry, err := NewRegistry([]Tool{readTool, writeTool, replaceTool, insertBeforeTool, insertAfterTool, bashTool})
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	names := make([]string, len(definitions))
	for i, definition := range definitions {
		names[i] = definition.Name
	}
	if !slices.Equal(names, []string{"bash", "insert_after", "insert_before", "read", "replace", "write"}) {
		t.Fatalf("definition names = %v", names)
	}
}

func executeJSON(t *testing.T, current Tool, arguments any) agent.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}

	result, err := current.Execute(context.Background(), encoded, nil)
	if err != nil {
		t.Fatalf("%s.Execute() error = %v", current.Definition().Name, err)
	}
	return result
}

type cancelAfterChecksContext struct {
	checks      int
	cancelAfter int
}

func (c *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (c *cancelAfterChecksContext) Value(any) any               { return nil }
func (c *cancelAfterChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}
