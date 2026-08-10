package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestResolveConfigLoadsDefaultAndExplicitModels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr)

	defaults, err := resolveTestConfig(Options{}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.model != "gpt-5.6-sol" || defaults.subagentFastModel != "gpt-5.6-luna" || defaults.subagentBalancedModel != "gpt-5.6-terra" || defaults.thinkingLevel != agent.DefaultThinkingLevel {
		t.Fatalf("resolved defaults = %+v", defaults)
	}

	explicit, err := resolveTestConfig(Options{
		Model:            "primary-model",
		ModelSet:         true,
		FastModel:        "fast-model",
		FastModelSet:     true,
		BalancedModel:    "balanced-model",
		BalancedModelSet: true,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.subagentFastModel != "fast-model" || explicit.subagentBalancedModel != "balanced-model" {
		t.Fatalf("config = %+v", explicit)
	}
}

func TestResolveConfigRejectsExplicitEmptyProfileModels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr)
	for _, options := range []Options{{FastModelSet: true}, {BalancedModelSet: true}} {
		if _, err := resolveTestConfig(options, runtime); err == nil || !strings.Contains(err.Error(), "model is required") {
			t.Fatalf("options %+v error = %v", options, err)
		}
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

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(root, &stdout, &stderr)
	runtime.userHomeDir = func() (string, error) { return home, nil }
	config, err := resolveTestConfig(Options{Model: "model", WorkingDirectory: "project"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" || config.skills[0].Description != "project" {
		t.Fatalf("skills = %+v", config.skills)
	}
	if len(config.warnings) != 1 || !strings.Contains(config.warnings[0], filepath.ToSlash(brokenSkill)) || !strings.Contains(config.warnings[0], "opening frontmatter") {
		t.Fatalf("warnings = %v", config.warnings)
	}
}

func TestResolveConfigSkipsGlobalSkillsWhenHomeIsUnavailable(t *testing.T) {
	cwd := t.TempDir()
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "project")

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr)
	runtime.userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }

	config, err := resolveTestConfig(Options{Model: "model"}, runtime)
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
	got, err := resolveCWD("", nil)
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
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(root, &stdout, &stderr)

	config, err := resolveTestConfig(Options{
		Model:            "gpt-5.6-sol",
		ThinkingLevel:    agent.ThinkingMax,
		WorkingDirectory: "project",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.model != "gpt-5.6-sol" || config.thinkingLevel != agent.ThinkingMax || config.cwd != cwd || config.projectInstructions != "Project rules." {
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
