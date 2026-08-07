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

func TestParseAgentArgumentsSelectsPromptSource(t *testing.T) {
	cwd := t.TempDir()
	for _, test := range []struct {
		name      string
		arguments []string
		input     string
		want      agentArguments
	}{
		{
			name:      "argument",
			arguments: []string{"--model", "gpt-5.6-sol", "--thinking", "high", "explain"},
			input:     "ignored",
			want:      agentArguments{model: "gpt-5.6-sol", thinkingLevel: agent.ThinkingHigh, prompt: "explain", oneShot: true},
		},
		{
			name:  "pipe",
			input: "from pipe\n",
			want:  agentArguments{model: "environment-model", thinkingLevel: agent.DefaultThinkingLevel, prompt: "from pipe\n", oneShot: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{"OPENAI_MODEL": "environment-model"})
			runtime.stdin = bytes.NewBufferString(test.input)

			got, err := parseAgentArguments(test.arguments, runtime)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("arguments = %+v, want %+v", got, test.want)
			}
		})
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
		prompt:        "inspect",
		oneShot:       true,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if config.model != "gpt-5.6-sol" || config.thinkingLevel != agent.ThinkingMax || config.cwd != cwd || config.projectInstructions != "Project rules." || config.prompt != "inspect" || !config.oneShot {
		t.Fatalf("config = %+v", config)
	}
}
