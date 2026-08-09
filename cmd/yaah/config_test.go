package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yaah/agent"
	openaiadapter "yaah/provider/openai"
)

func TestParseAgentArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, map[string]string{"OPENAI_MODEL": "environment-model"})

	got, err := parseAgentArguments([]string{"--model", "gpt-5.6-sol", "--thinking", "high", "--cwd", "project"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	want := agentArguments{model: "gpt-5.6-sol", thinkingLevel: agent.ThinkingHigh, cwd: "project"}
	if got != want {
		t.Fatalf("arguments = %+v, want %+v", got, want)
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
	writeConfigTestSkill(t, filepath.Join(root, ".agents", "skills", "parent", "SKILL.md"), "parent", "parent")
	writeConfigTestSkill(t, filepath.Join(cwd, ".pi", "skills", "pi", "SKILL.md"), "pi", "pi")

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(root, &stdout, &stderr, nil)
	runtime.userHomeDir = func() (string, error) { return home, nil }
	config, err := resolveAgentConfig(agentArguments{model: "model", cwd: "project"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" || config.skills[0].Description != "project" {
		t.Fatalf("skills = %+v", config.skills)
	}
}

func TestResolveAgentConfigSkipsGlobalSkillsWhenHomeIsUnavailable(t *testing.T) {
	cwd := t.TempDir()
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "project")

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	runtime.userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }

	config, err := resolveAgentConfig(agentArguments{model: "model"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.skills) != 1 || config.skills[0].Name != "review" {
		t.Fatalf("skills = %+v", config.skills)
	}
}

func TestOpenAIOptionsFromEnvironment(t *testing.T) {
	values := map[string]string{}
	options, err := openAIOptionsFromEnvironment(func(key string) string { return values[key] })
	if err != nil || options.ReasoningSummary != openaiadapter.ReasoningSummaryAuto {
		t.Fatalf("default options = %+v, err = %v", options, err)
	}

	values["OPENAI_REASONING_SUMMARY"] = "detailed"
	options, err = openAIOptionsFromEnvironment(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if options.ReasoningSummary != openaiadapter.ReasoningSummaryDetailed {
		t.Fatalf("reasoning summary = %q", options.ReasoningSummary)
	}

	values["OPENAI_REASONING_SUMMARY"] = "verbose"
	if _, err := openAIOptionsFromEnvironment(func(key string) string { return values[key] }); err == nil || !strings.Contains(err.Error(), "OPENAI_REASONING_SUMMARY") {
		t.Fatalf("invalid summary error = %v", err)
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

	config, err := resolveAgentConfig(agentArguments{
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
