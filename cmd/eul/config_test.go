package main

import (
	"bytes"
	"errors"
	"flag"
	"reflect"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/app"
	"github.com/eul-ai/eul/backend"
)

func TestParseAgentArguments(t *testing.T) {
	var stderr bytes.Buffer

	got, err := parseAgentArguments([]string{
		"--provider", "test",
		"--model", "gpt-5.6-sol",
		"--fast-model", "gpt-5.6-luna",
		"--balanced-model", "gpt-5.6-terra",
		"--thinking", "high",
		"--fast",
		"--cwd", "project",
		"--no-sandbox",
		"--prompt", "investigate the store",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := app.Options{
		Provider:         "test",
		Models:           backend.Models{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"},
		ThinkingLevel:    agent.ThinkingHigh,
		FastMode:         true,
		WorkingDirectory: "project",
		NoSandbox:        true,
		Prompt:           "investigate the store",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %+v, want %+v", got, want)
	}
}

func TestParseAgentArgumentsDefaults(t *testing.T) {
	var stderr bytes.Buffer

	got, err := parseAgentArguments(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "" || got.Models != (backend.Models{}) || got.ThinkingLevel != agent.ThinkingMedium {
		t.Fatalf("parsed defaults = %+v", got)
	}
}

func TestParseAgentArgumentsParsesResumeSelection(t *testing.T) {
	var stderr bytes.Buffer

	mostRecent, err := parseAgentArguments([]string{"--resume"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !mostRecent.Resume || mostRecent.SessionID != "" {
		t.Fatalf("most recent arguments = %+v", mostRecent)
	}

	explicit, err := parseAgentArguments([]string{"--resume=0123456789abcdef0123456789abcdef"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.Resume || explicit.SessionID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("explicit arguments = %+v", explicit)
	}

	for _, arguments := range [][]string{{"--resume", "--model", "other"}, {"--resume", "--fast"}} {
		if _, err := parseAgentArguments(arguments, &stderr); err == nil {
			t.Fatalf("arguments %v conflict error = %v", arguments, err)
		}
	}
}

func TestParseAgentArgumentsRejectsPrompts(t *testing.T) {
	var stderr bytes.Buffer

	if _, err := parseAgentArguments([]string{"prompt"}, &stderr); err == nil {
		t.Fatalf("error = %v", err)
	}
}

func TestParseAgentArgumentsReturnsFlagHelp(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseAgentArguments([]string{"--help"}, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
}
