package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool"
)

func TestSetExposesExpectedTools(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	cwd := t.TempDir()
	writeLSPTestConfig(t, cwd)

	for _, test := range []struct {
		includeRename bool
		want          []string
	}{
		{includeRename: true, want: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName, lspRenameToolName}},
		{want: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName}},
	} {
		set, err := New(cwd, "", test.includeRename)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, current := range set.Tools() {
			names = append(names, current.Definition().Name)
		}
		if !slices.Equal(names, test.want) {
			t.Fatalf("tools = %v, want %v", names, test.want)
		}
		if err := set.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLSPToolPresentationsUseSnakeCaseArguments(t *testing.T) {
	diagnostics := &lspTool{definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics}
	rename := &lspTool{definition: lspRenameToolDefinition, operation: lspRename}
	registry, err := tool.NewRegistry([]tool.Tool{diagnostics, rename})
	if err != nil {
		t.Fatal(err)
	}

	path := registry.Presentation(agent.ToolCallSnapshot{Name: lspDiagnosticsToolName, RawArguments: `{"path":"sample.go"}`, Complete: true})
	if path.Arguments != "sample.go" {
		t.Fatalf("diagnostics presentation = %+v", path)
	}
	names := registry.Presentation(agent.ToolCallSnapshot{Name: lspRenameToolName, RawArguments: `{"path":"sample.go","line":1,"character":2,"old_name":"Value","new_name":"Number"}`, Complete: true})
	if names.Arguments != "Value → Number" {
		t.Fatalf("rename presentation = %+v", names)
	}
}
