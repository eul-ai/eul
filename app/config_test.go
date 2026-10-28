package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/skill"
)

func TestResolveConfigLoadsDefaultAndExplicitModels(t *testing.T) {
	t.Chdir(t.TempDir())
	selected := testBackend("test", nil)

	defaults, err := resolveConfig(session.Settings{}, selected)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Provider != "test" || defaults.Models != (backend.Models{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"}) {
		t.Fatalf("resolved defaults = %+v", defaults)
	}

	explicit, err := resolveConfig(session.Settings{
		Models: backend.Models{Main: "main-model", Fast: "fast-model", Balanced: "balanced-model"},
	}, selected)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Models != (backend.Models{Main: "main-model", Fast: "fast-model", Balanced: "balanced-model"}) {
		t.Fatalf("config = %+v", explicit)
	}

	fastMode, err := resolveConfig(session.Settings{FastMode: true}, selected)
	if err != nil || !fastMode.FastMode {
		t.Fatalf("fast mode config = %+v, error = %v", fastMode, err)
	}
}

func TestResolveConfigRequiresMainModel(t *testing.T) {
	selected := backend.Backend{ID: "test"}
	if _, err := resolveConfig(session.Settings{Models: backend.Models{Fast: "fast-model"}}, selected); err == nil {
		t.Fatal("missing main model was accepted")
	}
}

func TestResolveConfigLoadsOnlyGenericSkillLocations(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigTestSkill(t, filepath.Join(home, ".agents", "skills", "review", "SKILL.md"), "review", "global")
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "project")
	brokenSkill := filepath.Join(cwd, ".agents", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(brokenSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenSkill, []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfigTestSkill(t, filepath.Join(root, ".agents", "skills", "parent", "SKILL.md"), "parent", "parent")
	writeConfigTestSkill(t, filepath.Join(cwd, ".pi", "skills", "pi", "SKILL.md"), "pi", "pi")

	t.Chdir(root)
	t.Setenv("HOME", home)
	config, err := resolveConfig(session.Settings{WorkingDirectory: "project"}, testBackend("test", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" || config.skills[0].Description != "project" {
		t.Fatalf("skills = %+v", config.skills)
	}
	if len(config.warnings) != 1 || !strings.Contains(config.warnings[0], filepath.ToSlash(brokenSkill)) {
		t.Fatalf("warnings = %v", config.warnings)
	}
}

func TestResolveConfigSkipsGlobalSkillsWhenHomeIsUnavailable(t *testing.T) {
	cwd := t.TempDir()
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "project")

	t.Chdir(cwd)
	t.Setenv("HOME", "")

	config, err := resolveConfig(session.Settings{}, testBackend("test", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" {
		t.Fatalf("skills = %+v", config.skills)
	}
}

func TestResolveCWDDefaultsGetwd(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveCWD("")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("working directory = %q, want %q", got, want)
	}
}

func TestResolveConfigLoadsProjectAndResolvesWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Project rules."), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	config, err := resolveConfig(session.Settings{
		Models:           backend.Models{Main: "gpt-5.6-sol"},
		ThinkingLevel:    agent.ThinkingMax,
		WorkingDirectory: "project",
	}, testBackend("test", nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.Models.Main != "gpt-5.6-sol" || config.ThinkingLevel != agent.ThinkingMax || config.WorkingDirectory != cwd || config.projectInstructions != "Project rules." {
		t.Fatalf("config = %+v", config)
	}
}

func writeConfigTestSkill(t *testing.T, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nInstructions\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPrompt(t *testing.T) {
	cwd := filepath.Join("workspace", "project")
	projectInstructions := "Run focused tests before finishing.\n"
	prompt := systemPrompt(resolvedConfig{
		Settings:            session.Settings{WorkingDirectory: cwd},
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
