package interactive

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
	"github.com/eul-ai/eul/skill"
)

type Options struct {
	Provider         string
	Model            *string
	FastModel        *string
	BalancedModel    *string
	ThinkingLevel    agent.ThinkingLevel
	FastMode         bool
	WorkingDirectory string
	NoSandbox        bool
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

type environment struct {
	stdin       io.Reader
	stdout      io.Writer
	getwd       func() (string, error)
	userHomeDir func() (string, error)
	interrupts  <-chan os.Signal
	backends    *backend.Registry
	newToolset  toolsetFactory
}

type modelSelection struct {
	main     string
	fast     string
	balanced string
}

type resolvedConfig struct {
	provider            string
	models              modelSelection
	thinkingLevel       agent.ThinkingLevel
	fastMode            bool
	cwd                 string
	projectInstructions string
	skills              []skill.Skill
	warnings            []string
	noSandbox           bool
}

func resolveConfig(options Options, env environment, descriptor backend.Descriptor) (resolvedConfig, error) {
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = agent.DefaultThinkingLevel
	}

	models, err := resolveModelSelection(options, descriptor.DefaultModels)
	if err != nil {
		return resolvedConfig{}, err
	}

	cwd, err := resolveCWD(options.WorkingDirectory, env.getwd)
	if err != nil {
		return resolvedConfig{}, err
	}
	projectInstructions, err := readProjectInstructions(cwd)
	if err != nil {
		return resolvedConfig{}, err
	}
	skillDirectories := []string{filepath.Join(cwd, ".agents", "skills")}
	if home := resolveUserHome(env.userHomeDir); home != "" {
		skillDirectories = append(skillDirectories, filepath.Join(home, ".agents", "skills"))
	}
	skills, warnings := skill.Load(skillDirectories...)

	return resolvedConfig{
		provider:            descriptor.ID,
		models:              models,
		thinkingLevel:       thinkingLevel,
		fastMode:            options.FastMode,
		cwd:                 cwd,
		projectInstructions: projectInstructions,
		skills:              skills,
		warnings:            warnings,
		noSandbox:           options.NoSandbox,
	}, nil
}

func resolveModelSelection(options Options, defaults backend.ModelDefaults) (modelSelection, error) {
	main, err := resolveConfiguredModel(options.Model, defaults.Main, "", "")
	if err != nil {
		return modelSelection{}, err
	}
	fast, err := resolveConfiguredModel(options.FastModel, defaults.Fast, main, "fast model")
	if err != nil {
		return modelSelection{}, err
	}
	balanced, err := resolveConfiguredModel(options.BalancedModel, defaults.Balanced, main, "balanced model")
	if err != nil {
		return modelSelection{}, err
	}
	return modelSelection{main: main, fast: fast, balanced: balanced}, nil
}

func resolveConfiguredModel(override *string, providerDefault, fallback, name string) (string, error) {
	value := providerDefault
	if override != nil {
		value = *override
	} else if value == "" {
		value = fallback
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
