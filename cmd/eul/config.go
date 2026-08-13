package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/app"
)

type reportedFlagError struct {
	error
}

func (err reportedFlagError) Unwrap() error {
	return err.error
}

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

func parseAgentArguments(arguments []string, runtime appRuntime) (app.Options, error) {
	flags := flag.NewFlagSet("eul", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	provider := flags.String("provider", "", "provider backend")
	model := flags.String("model", "", "main and powerful-subagent model (defaults to the provider configuration)")
	fastModel := flags.String("fast-model", "", "fast subagent model (defaults to the provider configuration)")
	balancedModel := flags.String("balanced-model", "", "balanced subagent model (defaults to the provider configuration)")
	thinking := flags.String("thinking", string(agent.DefaultThinkingLevel), "thinking level")
	fast := flags.Bool("fast", false, "enable the provider's fast inference mode")
	cwd := flags.String("cwd", "", "fixed working directory")
	noSandbox := flags.Bool("no-sandbox", false, "disable Bash network sandboxing and permission prompts")
	resume := &resumeValue{}
	flags.Var(resume, "resume", "resume the most recent session or a session selected with --resume=<id>")

	if err := flags.Parse(arguments); err != nil {
		return app.Options{}, reportedFlagError{error: err}
	}
	if flags.NArg() != 0 {
		return app.Options{}, errors.New("usage error: eul accepts no prompt arguments")
	}

	explicit := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { explicit[current.Name] = true })
	if resume.enabled && (explicit["provider"] || explicit["model"] || explicit["fast-model"] || explicit["balanced-model"] || explicit["thinking"] || explicit["fast"] || explicit["cwd"]) {
		return app.Options{}, errors.New("usage error: --resume cannot be combined with --provider, --model, --fast-model, --balanced-model, --thinking, --fast, or --cwd")
	}

	thinkingLevel := agent.DefaultThinkingLevel
	if !resume.enabled {
		var err error
		thinkingLevel, err = agent.ParseThinkingLevel(*thinking)
		if err != nil {
			return app.Options{}, err
		}
	}

	return app.Options{
		Provider:         *provider,
		Model:            optionalFlagValue(*model, explicit["model"]),
		FastModel:        optionalFlagValue(*fastModel, explicit["fast-model"]),
		BalancedModel:    optionalFlagValue(*balancedModel, explicit["balanced-model"]),
		ThinkingLevel:    thinkingLevel,
		FastMode:         *fast,
		WorkingDirectory: *cwd,
		NoSandbox:        *noSandbox,
		Resume:           resume.enabled,
		SessionID:        resume.sessionID,
	}, nil
}

func optionalFlagValue(value string, set bool) *string {
	if !set {
		return nil
	}
	return &value
}

func resolveEULHome(runtime appRuntime) (string, error) {
	if configured := runtime.getenv("EUL_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("EUL_HOME must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}

	userConfigDir := runtime.userConfigDir
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	config, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	if !filepath.IsAbs(config) {
		return "", errors.New("user config directory is not absolute")
	}
	return filepath.Join(filepath.Clean(config), "eul"), nil
}
