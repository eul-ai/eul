package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
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
		"--skip-permissions",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	want := session.Options{
		Provider:         "test",
		Model:            "gpt-5.6-sol",
		ModelSet:         true,
		FastModel:        "gpt-5.6-luna",
		FastModelSet:     true,
		BalancedModel:    "gpt-5.6-terra",
		BalancedModelSet: true,
		ThinkingLevel:    agent.ThinkingHigh,
		WorkingDirectory: "project",
		SkipPermissions:  true,
	}
	if got != want {
		t.Fatalf("arguments = %+v, want %+v", got, want)
	}
}

func TestParseAgentArgumentsDefaults(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	got, err := parseAgentArguments(nil, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "" || got.Model != "" || got.ModelSet || got.ThinkingLevel != agent.ThinkingMedium {
		t.Fatalf("parsed defaults = %+v", got)
	}
}

func TestParseAgentArgumentsParsesResumeSelection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	mostRecent, err := parseAgentArguments([]string{"--resume"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !mostRecent.Resume || mostRecent.SessionID != "" {
		t.Fatalf("most recent arguments = %+v", mostRecent)
	}

	explicit, err := parseAgentArguments([]string{"--resume=0123456789abcdef0123456789abcdef"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.Resume || explicit.SessionID != "0123456789abcdef0123456789abcdef" {
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

func TestParseAgentArgumentsReturnsFlagHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)
	if _, err := parseAgentArguments([]string{"--help"}, runtime); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
}
