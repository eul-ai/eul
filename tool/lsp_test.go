package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/protocol"

	"yaah/agent"
)

func TestNewLSPOmitsToolsWhenServerIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if tools := NewLSP(t.TempDir()); len(tools) != 0 {
		t.Fatalf("NewLSP() returned %d tools", len(tools))
	}
}

func TestNewLSPRegistersToolsWhenServerIsAvailable(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	if tools := NewLSP(t.TempDir()); len(tools) != 6 {
		t.Fatalf("NewLSP() returned %d tools", len(tools))
	}
}

func TestLSPToolsWithGopls(t *testing.T) {
	if _, err := exec.LookPath(lspServerConfigs[0].command); err != nil {
		t.Skip("gopls is not installed")
	}

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module example.com/sample\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const source = `package sample

type Thing struct {
	Value int
}

func Use(value Thing) int {
	return value.Value
}
`
	path := filepath.Join(cwd, "sample.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	tools := NewLSP(cwd)
	if len(tools) != 6 {
		t.Fatalf("NewLSP() returned %d tools", len(tools))
	}
	defer tools[0].(*lspTool).client.stop()
	registry := NewRegistry(tools...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	diagnostics := executeLSPTestTool(t, ctx, registry, lspDiagnosticsToolName, map[string]any{"path": "sample.go"})
	if !strings.Contains(diagnostics.Output, `"items": []`) {
		t.Fatalf("diagnostics = %s", diagnostics.Output)
	}

	thingLine, thingCharacter := sourcePosition(t, source, "func Use(value Thing)", "Thing")
	hover := executeLSPTestTool(t, ctx, registry, lspHoverToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(hover.Output, "Thing") {
		t.Fatalf("hover = %s", hover.Output)
	}

	definition := executeLSPTestTool(t, ctx, registry, lspDefinitionToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(definition.Output, "sample.go") {
		t.Fatalf("definition = %s", definition.Output)
	}

	references := executeLSPTestTool(t, ctx, registry, lspReferencesToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter, "includeDeclaration": true,
	})
	if strings.Count(references.Output, "sample.go") < 2 {
		t.Fatalf("references = %s", references.Output)
	}

	symbols := executeLSPTestTool(t, ctx, registry, lspSymbolsToolName, map[string]any{"path": "sample.go"})
	if !strings.Contains(symbols.Output, `"name": "Thing"`) || !strings.Contains(symbols.Output, `"name": "Use"`) {
		t.Fatalf("symbols = %s", symbols.Output)
	}

	valueLine, _ := sourcePosition(t, source, "return value.Value", "Value")
	rename := executeLSPTestTool(t, ctx, registry, lspRenameToolName, map[string]any{
		"path": "sample.go", "line": valueLine + 1, "character": 81, "oldName": "Value", "newName": "Number",
	})
	if rename.Output != "renamed symbol in 1 files" {
		t.Fatalf("rename = %s", rename.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "Number") != 2 || strings.Contains(string(updated), "Value") {
		t.Fatalf("renamed source:\n%s", updated)
	}
}

type blockingShutdownServer struct {
	protocol.UnimplementedServer
	done chan struct{}
}

func (s blockingShutdownServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	close(s.done)
	return ctx.Err()
}

func TestLSPShutdownIsBounded(t *testing.T) {
	server := blockingShutdownServer{done: make(chan struct{})}
	shutdownLSPServer(server, time.Millisecond)

	select {
	case <-server.done:
	default:
		t.Fatal("shutdown context did not expire")
	}
}

func TestLSPToolDescriptionsAreServerAgnostic(t *testing.T) {
	for _, definition := range []agent.ToolDefinition{
		lspDiagnosticsToolDefinition,
		lspHoverToolDefinition,
		lspDefinitionToolDefinition,
		lspReferencesToolDefinition,
		lspSymbolsToolDefinition,
		lspRenameToolDefinition,
	} {
		for _, config := range lspServerConfigs {
			if strings.Contains(strings.ToLower(definition.Description), strings.ToLower(config.name)) {
				t.Fatalf("%s description names server %q: %s", definition.Name, config.name, definition.Description)
			}
		}
	}
}

func TestLSPPositionOffsetUsesUTF16(t *testing.T) {
	content := []byte("a😀b\r\nnext")
	for _, test := range []struct {
		name     string
		position protocol.Position
		want     int
		wantErr  bool
	}{
		{name: "start", position: protocol.Position{}, want: 0},
		{name: "before surrogate pair", position: protocol.Position{Character: 1}, want: 1},
		{name: "after surrogate pair", position: protocol.Position{Character: 3}, want: 5},
		{name: "line end", position: protocol.Position{Character: 4}, want: 6},
		{name: "next line", position: protocol.Position{Line: 1}, want: 8},
		{name: "inside surrogate pair", position: protocol.Position{Character: 2}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := lspPositionOffset(content, test.position)
			if test.wantErr {
				if err == nil {
					t.Fatalf("offset = %d, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("offset=%d error=%v, want %d", got, err, test.want)
			}
		})
	}
}

func executeLSPTestTool(t *testing.T, ctx context.Context, registry *Registry, name string, arguments any) agent.ToolResult {
	t.Helper()

	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(ctx, agent.ToolCall{ID: "call", Name: name, Arguments: encoded}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s: %s", name, result.Output)
	}
	return result
}

func sourcePosition(t *testing.T, source, lineText, symbol string) (int, int) {
	t.Helper()

	lines := strings.Split(source, "\n")
	for line, text := range lines {
		if strings.Contains(text, lineText) {
			return line, strings.Index(text, symbol)
		}
	}
	t.Fatalf("line %q not found", lineText)
	return 0, 0
}
