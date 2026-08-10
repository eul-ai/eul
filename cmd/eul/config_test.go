package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestParseAgentArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	got, err := parseAgentArguments([]string{
		"--provider", "test",
		"--model", "gpt-5.6-sol",
		"--fast-model", "gpt-5.6-luna",
		"--balanced-model", "gpt-5.6-terra",
		"--thinking", "high",
		"--cwd", "project",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	want := agentArguments{
		provider:         "test",
		model:            "gpt-5.6-sol",
		modelSet:         true,
		fastModel:        "gpt-5.6-luna",
		fastModelSet:     true,
		balancedModel:    "gpt-5.6-terra",
		balancedModelSet: true,
		thinkingLevel:    agent.ThinkingHigh,
		cwd:              "project",
	}
	if got != want {
		t.Fatalf("arguments = %+v, want %+v", got, want)
	}
}

func TestParseAgentArgumentsDefaultsModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	got, err := parseAgentArguments(nil, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.model != "" || got.modelSet {
		t.Fatalf("parsed model = %q, explicitly set = %v", got.model, got.modelSet)
	}
	config, err := resolveTestAgentConfig(got, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.model != "gpt-5.6-sol" || config.subagentFastModel != "gpt-5.6-luna" || config.subagentBalancedModel != "gpt-5.6-terra" {
		t.Fatalf("resolved models = main %q, fast %q, balanced %q", config.model, config.subagentFastModel, config.subagentBalancedModel)
	}
}

func TestResolveAgentConfigLoadsSubagentModels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	config, err := resolveTestAgentConfig(agentArguments{
		model:            "primary-model",
		modelSet:         true,
		fastModel:        "fast-model",
		fastModelSet:     true,
		balancedModel:    "balanced-model",
		balancedModelSet: true,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.subagentFastModel != "fast-model" || config.subagentBalancedModel != "balanced-model" {
		t.Fatalf("config = %+v", config)
	}
}

func TestResolveAgentConfigRejectsExplicitEmptyProfileModels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)
	for _, flagName := range []string{"--fast-model=", "--balanced-model="} {
		arguments, err := parseAgentArguments([]string{flagName}, runtime)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolveTestAgentConfig(arguments, runtime); err == nil || !strings.Contains(err.Error(), "model is required") {
			t.Fatalf("flag %q error = %v", flagName, err)
		}
	}
}

func TestParseAgentArgumentsParsesResumeSelection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, map[string]string{"EUL_THINKING_LEVEL": "invalid-for-new-sessions"})

	mostRecent, err := parseAgentArguments([]string{"--resume"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !mostRecent.resume || mostRecent.sessionID != "" {
		t.Fatalf("most recent arguments = %+v", mostRecent)
	}

	explicit, err := parseAgentArguments([]string{"--resume=0123456789abcdef0123456789abcdef"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.resume || explicit.sessionID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("explicit arguments = %+v", explicit)
	}

	if _, err := parseAgentArguments([]string{"--resume", "--model", "other"}, runtime); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestParseAgentArgumentsRejectsPrompts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	if _, err := parseAgentArguments([]string{"prompt"}, runtime); err == nil || !strings.Contains(err.Error(), "no prompt arguments") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveAgentConfigLoadsOnlyGenericSkillLocations(t *testing.T) {
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
	runtime := testRuntime(root, &stdout, &stderr, nil)
	runtime.userHomeDir = func() (string, error) { return home, nil }
	config, err := resolveTestAgentConfig(agentArguments{model: "model", cwd: "project"}, runtime)
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

func TestResolveAgentConfigSkipsGlobalSkillsWhenHomeIsUnavailable(t *testing.T) {
	cwd := t.TempDir()
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "project")

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	runtime.userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }

	config, err := resolveTestAgentConfig(agentArguments{model: "model"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" {
		t.Fatalf("skills = %+v", config.skills)
	}
}

func TestParseAgentArgumentsReturnsFlagHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)
	if _, err := parseAgentArguments([]string{"--help"}, runtime); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
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

func TestResolveAgentConfigLoadsProjectAndResolvesWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Project rules."), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(root, &stdout, &stderr, nil)

	config, err := resolveTestAgentConfig(agentArguments{
		model:         "gpt-5.6-sol",
		thinkingLevel: agent.ThinkingMax,
		cwd:           "project",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.model != "gpt-5.6-sol" || config.thinkingLevel != agent.ThinkingMax || config.cwd != cwd || config.projectInstructions != "Project rules." {
		t.Fatalf("config = %+v", config)
	}
}
