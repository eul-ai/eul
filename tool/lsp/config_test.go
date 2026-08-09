package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lspTestConfig = `[
  {
    "name": "gopls",
    "command": "gopls",
    "languageID": "go",
    "extensions": [".go"]
  }
]
`

func TestNewLSPLoadsProjectConfig(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "custom-lsp"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	cwd := t.TempDir()
	content := `[
		{"name":"custom","command":"custom-lsp","arguments":["serve"],"languageID":"custom","extensions":[".custom"]}
	]`
	if err := os.WriteFile(filepath.Join(cwd, lspConfigFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if set.client == nil || len(set.client.configs) != 1 {
		t.Fatalf("client configs = %+v", set.client)
	}
	config := set.client.configs[0]
	if config.name != "custom" || config.command != "custom-lsp" || len(config.arguments) != 1 || config.arguments[0] != "serve" || config.languageID != "custom" || len(config.extensions) != 1 || config.extensions[0] != ".custom" {
		t.Fatalf("config = %+v", config)
	}
	if _, err := set.client.serverForPath("sample.custom"); err != nil {
		t.Fatalf("configured extension was not loaded: %v", err)
	}
}

func TestNewLSPReportsConfigErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		content *string
		want    string
	}{
		{name: "missing", want: "read lsp.json"},
		{name: "invalid", content: stringPointer("{"), want: "decode lsp.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			if test.content != nil {
				if err := os.WriteFile(filepath.Join(cwd, lspConfigFileName), []byte(*test.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			_, err := New(cwd)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want %q", err, test.want)
			}
		})
	}
}

func writeLSPTestConfig(t *testing.T, cwd string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, lspConfigFileName), []byte(lspTestConfig), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string {
	return &value
}
