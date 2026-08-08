package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPrompt(t *testing.T) {
	workingDirectory := filepath.Join("workspace", "project")
	projectInstructions := "Run focused tests before finishing.\n"
	prompt := buildSystemPrompt([]ToolDefinition{
		{Name: "read", Description: "Read file contents"},
		{Name: "write", Description: "Create or overwrite files"},
	}, workingDirectory, projectInstructions)

	if !strings.Contains(prompt, "- read: Read file contents") || !strings.Contains(prompt, "- write: Create or overwrite files") {
		t.Fatalf("prompt omits tools:\n%s", prompt)
	}
	instructionPath := filepath.ToSlash(filepath.Join(workingDirectory, "AGENTS.md"))
	wantInstructions := `<project_instructions path="` + instructionPath + `">` + "\n" + strings.TrimSuffix(projectInstructions, "\n") + "\n</project_instructions>"
	if !strings.Contains(prompt, wantInstructions) {
		t.Fatalf("prompt does not identify loaded project instructions:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Current working directory: "+filepath.ToSlash(workingDirectory)) {
		t.Fatalf("prompt omits working directory:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithNoToolsOrWorkingDirectory(t *testing.T) {
	prompt := buildSystemPrompt(nil, "", "")

	if !strings.Contains(prompt, "Available tools:\n(none)") {
		t.Fatalf("prompt does not identify an empty toolset:\n%s", prompt)
	}
	if strings.Contains(prompt, "Current working directory:") {
		t.Fatalf("prompt unexpectedly identifies a working directory:\n%s", prompt)
	}
}
