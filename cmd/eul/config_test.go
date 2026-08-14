package main

import (
	"bytes"
	"errors"
	"flag"
	"reflect"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/app"
)

func stringPointer(value string) *string {
	return &value
}

func TestParseAgentArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	got, err := parseAgentArguments([]string{
		"--provider", "test",
		"--model", "gpt-5.6-sol",
		"--fast-model", "gpt-5.6-luna",
		"--balanced-model", "gpt-5.6-terra",
		"--thinking", "high",
		"--fast",
		"--cwd", "project",
		"--no-sandbox",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	want := app.Options{
		Provider:         "test",
		Model:            stringPointer("gpt-5.6-sol"),
		FastModel:        stringPointer("gpt-5.6-luna"),
		BalancedModel:    stringPointer("gpt-5.6-terra"),
		ThinkingLevel:    agent.ThinkingHigh,
		FastMode:         true,
		WorkingDirectory: "project",
		NoSandbox:        true,
	}
	if !reflect.DeepEqual(got, want) {
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
	if got.Provider != "" || got.Model != nil || got.ThinkingLevel != agent.ThinkingMedium {
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

	for _, arguments := range [][]string{{"--resume", "--model", "other"}, {"--resume", "--fast"}} {
		if _, err := parseAgentArguments(arguments, runtime); err == nil {
			t.Fatalf("arguments %v conflict error = %v", arguments, err)
		}
	}
}

func TestParseAgentArgumentsRejectsPrompts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)

	if _, err := parseAgentArguments([]string{"prompt"}, runtime); err == nil {
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
