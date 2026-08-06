package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt(t *testing.T) {
	prompt := buildSystemPrompt([]ToolDefinition{
		{Name: "read", Description: "Read file contents"},
		{Name: "write", Description: "Create or overwrite files"},
	}, "")

	if !strings.Contains(prompt, "- read: Read file contents") || !strings.Contains(prompt, "- write: Create or overwrite files") {
		t.Fatalf("prompt omits tools:\n%s", prompt)
	}
	for _, excluded := range []string{"working directory", "AGENTS.md", "CLAUDE.md"} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("MVP prompt unexpectedly contains %q:\n%s", excluded, prompt)
		}
	}
}

func TestBuildSystemPromptWithNoTools(t *testing.T) {
	prompt := buildSystemPrompt(nil, "")

	if !strings.Contains(prompt, "Available tools:\n(none)") {
		t.Fatalf("prompt does not identify an empty toolset:\n%s", prompt)
	}
}
