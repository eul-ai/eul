package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

const lspToolTestConfig = `[{"name":"gopls","command":"gopls","languageID":"go","extensions":[".go"]}]`

func TestNewLSPRegistersFullAndReadOnlyTools(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "lsp.json"), []byte(lspToolTestConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name          string
		includeRename bool
		wantNames     []string
	}{
		{name: "full", includeRename: true, wantNames: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName, lspRenameToolName}},
		{name: "read-only", wantNames: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools, service, err := NewLSP(cwd, "", test.includeRename)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			if len(tools) != len(test.wantNames) {
				t.Fatalf("tool count = %d, want %d", len(tools), len(test.wantNames))
			}
			for index, current := range tools {
				if current.Definition().Name != test.wantNames[index] {
					t.Fatalf("tool %d = %q, want %q", index, current.Definition().Name, test.wantNames[index])
				}
			}
		})
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
		if strings.Contains(strings.ToLower(definition.Description), "gopls") {
			t.Fatalf("%s description names gopls: %s", definition.Name, definition.Description)
		}
	}
}

func TestLSPDiagnosticsPresentationShowsPath(t *testing.T) {
	diagnostics := &lspTool{definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics}
	registry, err := NewRegistry([]Tool{diagnostics})
	if err != nil {
		t.Fatal(err)
	}

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		Name:         lspDiagnosticsToolName,
		RawArguments: `{"path":"sample.go"}`,
		Complete:     true,
	})
	if presentation.Title != lspDiagnosticsToolName || presentation.Arguments != "sample.go" {
		t.Fatalf("diagnostics presentation = %+v", presentation)
	}
}

func TestLSPRenamePresentationShowsNames(t *testing.T) {
	rename := &lspTool{definition: lspRenameToolDefinition, operation: lspRename}
	registry, err := NewRegistry([]Tool{rename})
	if err != nil {
		t.Fatal(err)
	}

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		Name:         lspRenameToolName,
		RawArguments: `{"path":"sample.go","line":1,"character":2,"oldName":"Value","newName":"Number"}`,
		Complete:     true,
	})
	if presentation.Title != lspRenameToolName || presentation.Arguments != "Value → Number" {
		t.Fatalf("rename presentation = %+v", presentation)
	}
}
