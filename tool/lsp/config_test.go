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
	writeExecutable(t, filepath.Join(bin, "custom-lsp"))
	t.Setenv("PATH", bin)

	cwd := t.TempDir()
	content := `[
		{"name":"custom","command":"custom-lsp","arguments":["serve"],"languageID":"custom","extensions":[".CUSTOM"]}
	]`
	writeLSPConfig(t, cwd, content)

	set, err := New(cwd, t.TempDir())
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

func TestNewLSPPrefersEULHomeConfig(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "home-lsp"))
	writeExecutable(t, filepath.Join(bin, "project-lsp"))
	t.Setenv("PATH", bin)

	home := t.TempDir()
	cwd := t.TempDir()
	writeLSPConfig(t, home, `[{"name":"home","command":"home-lsp","languageID":"home","extensions":[".home"]}]`)
	writeLSPConfig(t, cwd, `[{"name":"project","command":"project-lsp","languageID":"project","extensions":[".project"]}]`)

	set, err := New(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if set.client == nil || len(set.client.configs) != 1 || set.client.configs[0].name != "home" {
		t.Fatalf("client configs = %+v", set.client)
	}
}

func TestNewLSPWithoutConfigReturnsEmptySet(t *testing.T) {
	set, err := New(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if set.client != nil || len(set.Tools()) != 0 {
		t.Fatalf("set = %+v, tools = %+v", set, set.Tools())
	}
}

func TestNewLSPReportsInvalidConfig(t *testing.T) {
	cwd := t.TempDir()
	writeLSPConfig(t, cwd, "{")

	_, err := New(cwd, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "decode lsp.json") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestLSPConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "name", content: `[{"command":"lsp","languageID":"go","extensions":[".go"]}]`, want: "invalid name"},
		{name: "command", content: `[{"name":"go","languageID":"go","extensions":[".go"]}]`, want: "invalid command"},
		{name: "language", content: `[{"name":"go","command":"lsp","extensions":[".go"]}]`, want: "invalid languageID"},
		{name: "extensions", content: `[{"name":"go","command":"lsp","languageID":"go"}]`, want: "no extensions"},
		{name: "duplicate name", content: `[{"name":"go","command":"one","languageID":"go","extensions":[".go"]},{"name":"go","command":"two","languageID":"go","extensions":[".mod"]}]`, want: "name \"go\" is duplicated"},
		{name: "invalid extension", content: `[{"name":"go","command":"lsp","languageID":"go","extensions":["go"]}]`, want: "invalid extension"},
		{name: "duplicate extension", content: `[{"name":"one","command":"one","languageID":"go","extensions":[".go"]},{"name":"two","command":"two","languageID":"go","extensions":[".GO"]}]`, want: "configured for both"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeLSPConfig(t, directory, test.content)

			_, err := loadLSPServerConfigs(filepath.Join(directory, lspConfigFileName))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadLSPServerConfigs() error = %v, want %q", err, test.want)
			}
		})
	}
}

func writeLSPTestConfig(t *testing.T, cwd string) {
	t.Helper()
	writeLSPConfig(t, cwd, lspTestConfig)
}

func writeLSPConfig(t *testing.T, directory, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, lspConfigFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}
