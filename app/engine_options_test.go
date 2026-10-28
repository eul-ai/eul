package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/skill"
)

func TestSystemPrompt(t *testing.T) {
	cwd := filepath.Join("workspace", "project")
	projectInstructions := "Run focused tests before finishing.\n"
	prompt := systemPrompt(resolvedConfig{
		sessionSettings:     sessionSettings{cwd: cwd},
		projectInstructions: projectInstructions,
		skills:              []skill.Skill{{Name: "review", Description: "Review code", FilePath: "/skills/review/SKILL.md"}},
	})

	instructionPath := filepath.ToSlash(filepath.Join(cwd, "AGENTS.md"))
	wantInstructions := `<project_instructions path="` + instructionPath + `">` + "\n" + strings.TrimSuffix(projectInstructions, "\n") + "\n</project_instructions>"
	if !strings.Contains(prompt, wantInstructions) {
		t.Fatalf("prompt does not identify loaded project instructions:\n%s", prompt)
	}
	if strings.Count(prompt, filepath.ToSlash(cwd)) < 2 {
		t.Fatalf("prompt omits standalone working directory value:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<name>review</name>") {
		t.Fatalf("prompt omits skills:\n%s", prompt)
	}
}
