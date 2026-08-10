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
	openaiadapter "github.com/eul-ai/eul/provider/openai"
)

const defaultModel = "gpt-5.6-sol"

type reportedFlagError struct {
	error
}

func (err reportedFlagError) Unwrap() error {
	return err.error
}

type agentArguments struct {
	model         string
	thinkingLevel agent.ThinkingLevel
	cwd           string
	resume        bool
	sessionID     string
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

	modelDefault := runtime.getenv("OPENAI_MODEL")
	if modelDefault == "" {
		modelDefault = defaultModel
	}

	flags := flag.NewFlagSet("eul", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	model := flags.String("model", modelDefault, "OpenAI model (or OPENAI_MODEL)")
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
	if resume.enabled && (explicit["model"] || explicit["thinking"] || explicit["cwd"]) {
		return agentArguments{}, errors.New("usage error: --resume cannot be combined with --model, --thinking, or --cwd")
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
		model:         *model,
		thinkingLevel: thinkingLevel,
		cwd:           *cwd,
		resume:        resume.enabled,
		sessionID:     resume.sessionID,
	}, nil
}

func resolveAgentConfig(arguments agentArguments, runtime appRuntime) (agentConfig, error) {
	if err := validateModel(arguments.model); err != nil {
		return agentConfig{}, err
	}
	subagentFastModel, err := modelFromEnvironment(runtime.getenv, "OPENAI_MODEL_FAST", arguments.model)
	if err != nil {
		return agentConfig{}, err
	}
	subagentBalancedModel, err := modelFromEnvironment(runtime.getenv, "OPENAI_MODEL_BALANCED", arguments.model)
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
		model:                 arguments.model,
		subagentFastModel:     subagentFastModel,
		subagentBalancedModel: subagentBalancedModel,
		thinkingLevel:         arguments.thinkingLevel,
		cwd:                   cwd,
		projectInstructions:   projectInstructions,
		skills:                skills,
		warnings:              warnings,
	}, nil
}

func modelFromEnvironment(getenv func(string) string, name, fallback string) (string, error) {
	if getenv == nil {
		return fallback, nil
	}
	model := getenv(name)
	if model == "" {
		return fallback, nil
	}
	if err := validateModel(model); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return model, nil
}

func openAIOptionsFromEnvironment(getenv func(string) string) (openaiadapter.Options, error) {
	summary, err := openaiadapter.ParseReasoningSummary(getenv("OPENAI_REASONING_SUMMARY"))
	if err != nil {
		return openaiadapter.Options{}, fmt.Errorf("OPENAI_REASONING_SUMMARY: %w", err)
	}
	return openaiadapter.Options{ReasoningSummary: summary}, nil
}

func validateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model is required; use --model or OPENAI_MODEL")
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
