package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/skill"
)

func TestBuildSystemPrompt(t *testing.T) {
	workingDirectory := filepath.Join("workspace", "project")
	projectInstructions := "Run focused tests before finishing.\n"
	prompt := buildSystemPrompt([]ToolDefinition{
		{Name: "read", Description: "Read file contents"},
		{Name: "write", Description: "Create or overwrite files"},
	}, workingDirectory, projectInstructions, nil)

	if strings.Contains(prompt, "Available tools:") || strings.Contains(prompt, "Read file contents") || strings.Contains(prompt, "Create or overwrite files") {
		t.Fatalf("prompt duplicates tool definitions:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Independent tool calls may run concurrently; separate dependent calls.") {
		t.Fatalf("prompt omits concurrent tool guidance:\n%s", prompt)
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
	prompt := buildSystemPrompt(nil, "", "", []skill.Skill{{Name: "review", Description: "Review code", FilePath: "/skills/review/SKILL.md"}})

	if strings.Contains(prompt, "concurrently") {
		t.Fatalf("prompt includes tool guidance without tools:\n%s", prompt)
	}
	if strings.Contains(prompt, "Current working directory:") {
		t.Fatalf("prompt unexpectedly identifies a working directory:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<name>review</name>") {
		t.Fatalf("prompt omits skills:\n%s", prompt)
	}
}
