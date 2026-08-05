package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptIsDeterministic(t *testing.T) {
	definitions := []ToolDefinition{
		{
			Name:             "write",
			Description:      "write description",
			PromptSummary:    "Create or overwrite files",
			PromptGuidelines: []string{"Use write for complete rewrites.", "Shared guidance."},
		},
		{
			Name:             "read",
			Description:      "Read file contents",
			PromptGuidelines: []string{"Inspect files before editing.", "Shared guidance."},
		},
	}

	forward := BuildSystemPrompt(definitions)
	reverse := BuildSystemPrompt([]ToolDefinition{definitions[1], definitions[0]})
	if forward != reverse {
		t.Fatalf("prompt depends on tool registration order:\nforward:\n%s\nreverse:\n%s", forward, reverse)
	}

	readIndex := strings.Index(forward, "- read: Read file contents")
	writeIndex := strings.Index(forward, "- write: Create or overwrite files")
	if readIndex < 0 || writeIndex < 0 || readIndex > writeIndex {
		t.Fatalf("tools are not rendered in deterministic name order:\n%s", forward)
	}
	if strings.Count(forward, "Shared guidance.") != 1 {
		t.Fatalf("duplicate guidance was not removed:\n%s", forward)
	}
	for _, excluded := range []string{"working directory", "AGENTS.md", "CLAUDE.md"} {
		if strings.Contains(forward, excluded) {
			t.Fatalf("MVP prompt unexpectedly contains %q:\n%s", excluded, forward)
		}
	}
}

func TestBuildSystemPromptDuplicateNamesRemainDeterministic(t *testing.T) {
	first := ToolDefinition{Name: "read", PromptSummary: "Z summary", PromptGuidelines: []string{"Z guidance"}}
	second := ToolDefinition{Name: "read", PromptSummary: "A summary", PromptGuidelines: []string{"A guidance"}}

	forward := BuildSystemPrompt([]ToolDefinition{first, second})
	reverse := BuildSystemPrompt([]ToolDefinition{second, first})
	if forward != reverse {
		t.Fatalf("prompt with duplicate names depends on input order:\nforward:\n%s\nreverse:\n%s", forward, reverse)
	}
}

func TestBuildSystemPromptWithNoTools(t *testing.T) {
	prompt := BuildSystemPrompt(nil)
	if !strings.Contains(prompt, "Available tools:\n(none)") {
		t.Fatalf("prompt does not identify an empty toolset:\n%s", prompt)
	}
}
