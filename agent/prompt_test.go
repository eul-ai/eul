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
	descriptions := []string{"opaque-read-description", "opaque-write-description"}
	prompt := buildSystemPrompt([]ToolDefinition{
		{Name: "read", Description: descriptions[0]},
		{Name: "write", Description: descriptions[1]},
	}, workingDirectory, projectInstructions, nil)

	for _, description := range descriptions {
		if strings.Contains(prompt, description) {
			t.Fatalf("prompt duplicates tool description %q:\n%s", description, prompt)
		}
	}
	instructionPath := filepath.ToSlash(filepath.Join(workingDirectory, "AGENTS.md"))
	wantInstructions := `<project_instructions path="` + instructionPath + `">` + "\n" + strings.TrimSuffix(projectInstructions, "\n") + "\n</project_instructions>"
	if !strings.Contains(prompt, wantInstructions) {
		t.Fatalf("prompt does not identify loaded project instructions:\n%s", prompt)
	}
	if strings.Count(prompt, filepath.ToSlash(workingDirectory)) < 2 {
		t.Fatalf("prompt omits standalone working directory value:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithNoToolsOrWorkingDirectory(t *testing.T) {
	prompt := buildSystemPrompt(nil, "", "", []skill.Skill{{Name: "review", Description: "Review code", FilePath: "/skills/review/SKILL.md"}})

	if !strings.Contains(prompt, "<name>review</name>") {
		t.Fatalf("prompt omits skills:\n%s", prompt)
	}
}
