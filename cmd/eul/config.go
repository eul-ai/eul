package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type reportedFlagError struct {
	error
}

func (err reportedFlagError) Unwrap() error {
	return err.error
}

type agentArguments struct {
	provider         string
	model            string
	modelSet         bool
	fastModel        string
	fastModelSet     bool
	balancedModel    string
	balancedModelSet bool
	thinkingLevel    agent.ThinkingLevel
	cwd              string
	resume           bool
	sessionID        string
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

type agentConfig struct {
	provider              string
	model                 string
	subagentFastModel     string
	subagentBalancedModel string
	thinkingLevel         agent.ThinkingLevel
	cwd                   string
	projectInstructions   string
	skills                []agent.Skill
	warnings              []string
}

func parseAgentArguments(arguments []string, runtime appRuntime) (agentArguments, error) {
	thinkingDefault := runtime.getenv("EUL_THINKING_LEVEL")
	if thinkingDefault == "" {
		thinkingDefault = string(agent.DefaultThinkingLevel)
	}

	flags := flag.NewFlagSet("eul", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	provider := flags.String("provider", runtime.getenv("EUL_PROVIDER"), "provider backend (or EUL_PROVIDER)")
	model := flags.String("model", "", "main and powerful-subagent model (defaults to the provider configuration)")
	fastModel := flags.String("fast-model", "", "fast subagent model (defaults to the provider configuration)")
	balancedModel := flags.String("balanced-model", "", "balanced subagent model (defaults to the provider configuration)")
	thinking := flags.String("thinking", thinkingDefault, "thinking level (or EUL_THINKING_LEVEL)")
	cwd := flags.String("cwd", "", "fixed working directory")
	resume := &resumeValue{}
	flags.Var(resume, "resume", "resume the most recent session or a session selected with --resume=<id>")

	if err := flags.Parse(arguments); err != nil {
		return agentArguments{}, reportedFlagError{error: err}
	}
	if flags.NArg() != 0 {
		return agentArguments{}, errors.New("usage error: eul accepts no prompt arguments")
	}

	explicit := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { explicit[current.Name] = true })
	if resume.enabled && (explicit["provider"] || explicit["model"] || explicit["fast-model"] || explicit["balanced-model"] || explicit["thinking"] || explicit["cwd"]) {
		return agentArguments{}, errors.New("usage error: --resume cannot be combined with --provider, --model, --fast-model, --balanced-model, --thinking, or --cwd")
	}

	thinkingLevel := agent.DefaultThinkingLevel
	if !resume.enabled {
		var err error
		thinkingLevel, err = agent.ParseThinkingLevel(*thinking)
		if err != nil {
			return agentArguments{}, err
		}
	}

	return agentArguments{
		provider:         *provider,
		model:            *model,
		modelSet:         explicit["model"],
		fastModel:        *fastModel,
		fastModelSet:     explicit["fast-model"],
		balancedModel:    *balancedModel,
		balancedModelSet: explicit["balanced-model"],
		thinkingLevel:    thinkingLevel,
		cwd:              *cwd,
		resume:           resume.enabled,
		sessionID:        resume.sessionID,
	}, nil
}

func resolveAgentConfig(arguments agentArguments, runtime appRuntime, descriptor backend.Descriptor, defaults backend.ModelDefaults) (agentConfig, error) {
	model, err := resolveConfiguredModel(arguments.model, arguments.modelSet, defaults.Main, "", "")
	if err != nil {
		return agentConfig{}, err
	}
	subagentFastModel, err := resolveConfiguredModel(arguments.fastModel, arguments.fastModelSet, defaults.Fast, model, "fast model")
	if err != nil {
		return agentConfig{}, err
	}
	subagentBalancedModel, err := resolveConfiguredModel(arguments.balancedModel, arguments.balancedModelSet, defaults.Balanced, model, "balanced model")
	if err != nil {
		return agentConfig{}, err
	}

	cwd, err := resolveCWD(arguments.cwd, runtime.getwd)
	if err != nil {
		return agentConfig{}, err
	}
	projectInstructions, err := readProjectInstructions(cwd)
	if err != nil {
		return agentConfig{}, err
	}
	skillDirectories := []string{filepath.Join(cwd, ".agents", "skills")}
	if home := resolveUserHome(runtime.userHomeDir); home != "" {
		skillDirectories = append(skillDirectories, filepath.Join(home, ".agents", "skills"))
	}
	skills, warnings := agent.LoadSkills(skillDirectories...)

	return agentConfig{
		provider:              descriptor.ID,
		model:                 model,
		subagentFastModel:     subagentFastModel,
		subagentBalancedModel: subagentBalancedModel,
		thinkingLevel:         arguments.thinkingLevel,
		cwd:                   cwd,
		projectInstructions:   projectInstructions,
		skills:                skills,
		warnings:              warnings,
	}, nil
}

func resolveConfiguredModel(value string, set bool, providerDefault, fallback, name string) (string, error) {
	if !set && value == "" {
		value = providerDefault
		if value == "" {
			value = fallback
		}
	}
	if err := validateModel(value); err != nil {
		if name == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func validateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model is required; use --model or configure the selected provider")
	}

	if model != strings.TrimSpace(model) || strings.IndexFunc(model, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) >= 0 {
		return errors.New("model must not contain whitespace or control characters")
	}

	return nil
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

func resolveUserHome(userHomeDir func() (string, error)) string {
	if userHomeDir == nil {
		userHomeDir = os.UserHomeDir
	}
	home, err := userHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Clean(home)
}

func resolveCWD(value string, getwd func() (string, error)) (string, error) {
	candidate := value
	if candidate == "" || !filepath.IsAbs(candidate) {
		base, err := getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
		if !filepath.IsAbs(base) {
			base, err = filepath.Abs(base)
			if err != nil {
				return "", fmt.Errorf("resolve working directory: %w", err)
			}
		}
		candidate = filepath.Join(base, candidate)
	}

	candidate = filepath.Clean(candidate)
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("inspect working directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", candidate)
	}
	return candidate, nil
}

func readProjectInstructions(cwd string) (string, error) {
	content, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read AGENTS.md: %w", err)
	}
	return string(content), nil
}
