package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/skill"
)

type Options struct {
	Provider         string
	Models           backend.Models
	ThinkingLevel    agent.ThinkingLevel
	FastMode         bool
	WorkingDirectory string
	NoSandbox        bool
	Resume           bool
	SessionID        string
	Prompt           string
}

type Dependencies struct {
	Input       io.Reader
	Output      io.Writer
	Home        string
	Getwd       func() (string, error)
	UserHomeDir func() (string, error)
	Interrupts  <-chan os.Signal
	Backends    backend.Registry
}

type environment struct {
	stdin       io.Reader
	stdout      io.Writer
	getwd       func() (string, error)
	userHomeDir func() (string, error)
	interrupts  <-chan os.Signal
	backends    backend.Registry
	newToolset  toolsetFactory
}

type sessionSettings struct {
	provider      string
	models        backend.Models
	thinkingLevel agent.ThinkingLevel
	fastMode      bool
	cwd           string
}

type resolvedConfig struct {
	sessionSettings
	projectInstructions string
	skills              []skill.Skill
	warnings            []string
}

func resolveConfig(settings sessionSettings, selected backend.Backend, env environment) (resolvedConfig, error) {
	settings.provider = selected.ID
	settings.models = settings.models.WithDefaults(selected.Models)
	if settings.models.Main == "" {
		return resolvedConfig{}, errors.New("main model is required; use --model")
	}

	cwd, err := resolveCWD(settings.cwd, env.getwd)
	if err != nil {
		return resolvedConfig{}, err
	}
	settings.cwd = cwd
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
		sessionSettings:     settings,
		projectInstructions: projectInstructions,
		skills:              skills,
		warnings:            warnings,
	}, nil
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
