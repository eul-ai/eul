package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/app"
	"github.com/eul-ai/eul/backend"
)

type resumeValue struct {
	enabled   bool
	sessionID string
}

func (value *resumeValue) String() string {
	return value.sessionID
}

func (value *resumeValue) Set(raw string) error {
	switch raw {
	case "false":
		value.enabled = false
		value.sessionID = ""
	case "true", "":
		value.enabled = true
		value.sessionID = ""
	default:
		value.enabled = true
		value.sessionID = raw
	}
	return nil
}

func (*resumeValue) IsBoolFlag() bool {
	return true
}

// parseAgentArguments reports its errors to stderr.
func parseAgentArguments(arguments []string, stderr io.Writer) (app.Options, error) {
	flags := flag.NewFlagSet("eul", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "provider backend")
	model := flags.String("model", "", "main agent model, also used by main-profile subagents (defaults to the provider configuration)")
	fastModel := flags.String("fast-model", "", "fast subagent model (defaults to the provider configuration)")
	balancedModel := flags.String("balanced-model", "", "balanced subagent model (defaults to the provider configuration)")
	thinking := flags.String("thinking", string(agent.DefaultThinkingLevel), "thinking level")
	fast := flags.Bool("fast", false, "enable the provider's fast inference mode")
	cwd := flags.String("cwd", "", "fixed working directory")
	noSandbox := flags.Bool("no-sandbox", false, "disable Bash network sandboxing and permission prompts")
	prompt := flags.String("prompt", "", "start the session by sending this message")
	resume := &resumeValue{}
	flags.Var(resume, "resume", "resume the most recent session or a session selected with --resume=<id>")

	if err := flags.Parse(arguments); err != nil {
		return app.Options{}, err
	}

	explicit := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { explicit[current.Name] = true })
	thinkingLevel, err := agent.ParseThinkingLevel(*thinking)
	switch {
	case flags.NArg() != 0:
		err = errors.New("usage error: eul accepts no prompt arguments")
	case resume.enabled && (explicit["provider"] || explicit["model"] || explicit["fast-model"] || explicit["balanced-model"] || explicit["thinking"] || explicit["fast"] || explicit["cwd"]):
		err = errors.New("usage error: --resume cannot be combined with --provider, --model, --fast-model, --balanced-model, --thinking, --fast, or --cwd")
	}
	if err != nil {
		writeCLIError(stderr, "%v", err)
		return app.Options{}, err
	}

	return app.Options{
		Provider:         *provider,
		Models:           backend.Models{Main: *model, Fast: *fastModel, Balanced: *balancedModel},
		ThinkingLevel:    thinkingLevel,
		FastMode:         *fast,
		WorkingDirectory: *cwd,
		NoSandbox:        *noSandbox,
		Resume:           resume.enabled,
		SessionID:        resume.sessionID,
		Prompt:           *prompt,
	}, nil
}

func resolveEULHome() (string, error) {
	if configured := os.Getenv("EUL_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("EUL_HOME must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}

	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	if !filepath.IsAbs(config) {
		return "", errors.New("user config directory is not absolute")
	}
	return filepath.Join(filepath.Clean(config), "eul"), nil
}
