package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type Options struct {
	Provider         string
	Model            string
	ModelSet         bool
	FastModel        string
	FastModelSet     bool
	BalancedModel    string
	BalancedModelSet bool
	ThinkingLevel    agent.ThinkingLevel
	WorkingDirectory string
	Resume           bool
	SessionID        string
}

type Dependencies struct {
	Input       io.Reader
	Output      io.Writer
	Home        string
	Getwd       func() (string, error)
	UserHomeDir func() (string, error)
	Interrupts  <-chan os.Signal
	Backends    *backend.Registry
}

type runtime struct {
	stdin       io.Reader
	stdout      io.Writer
	getwd       func() (string, error)
	userHomeDir func() (string, error)
	interrupts  <-chan os.Signal
	backends    *backend.Registry
	newToolset  toolsetFactory
}

type config struct {
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

func resolveConfig(options Options, runtime runtime, descriptor backend.Descriptor, defaults backend.ModelDefaults) (config, error) {
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = agent.DefaultThinkingLevel
	}

	model, err := resolveConfiguredModel(options.Model, options.ModelSet, defaults.Main, "", "")
	if err != nil {
		return config{}, err
	}
	subagentFastModel, err := resolveConfiguredModel(options.FastModel, options.FastModelSet, defaults.Fast, model, "fast model")
	if err != nil {
		return config{}, err
	}
	subagentBalancedModel, err := resolveConfiguredModel(options.BalancedModel, options.BalancedModelSet, defaults.Balanced, model, "balanced model")
	if err != nil {
		return config{}, err
	}

	cwd, err := resolveCWD(options.WorkingDirectory, runtime.getwd)
	if err != nil {
		return config{}, err
	}
	projectInstructions, err := readProjectInstructions(cwd)
	if err != nil {
		return config{}, err
	}
	skillDirectories := []string{filepath.Join(cwd, ".agents", "skills")}
	if home := resolveUserHome(runtime.userHomeDir); home != "" {
		skillDirectories = append(skillDirectories, filepath.Join(home, ".agents", "skills"))
	}
	skills, warnings := agent.LoadSkills(skillDirectories...)

	return config{
		provider:              descriptor.ID,
		model:                 model,
		subagentFastModel:     subagentFastModel,
		subagentBalancedModel: subagentBalancedModel,
		thinkingLevel:         thinkingLevel,
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
		if getwd == nil {
			getwd = os.Getwd
		}
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
