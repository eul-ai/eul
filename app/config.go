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
	Input      io.Reader
	Output     io.Writer
	Home       string
	Interrupts <-chan os.Signal
	Backends   backend.Registry
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

func resolveConfig(settings sessionSettings, selected backend.Backend) (resolvedConfig, error) {
	settings.provider = selected.ID
	settings.models = settings.models.WithDefaults(selected.Models)
	if settings.models.Main == "" {
		return resolvedConfig{}, errors.New("main model is required; use --model")
	}

	cwd, err := resolveCWD(settings.cwd)
	if err != nil {
		return resolvedConfig{}, err
	}
	settings.cwd = cwd
	projectInstructions, err := readProjectInstructions(cwd)
	if err != nil {
		return resolvedConfig{}, err
	}
	skillDirectories := []string{filepath.Join(cwd, ".agents", "skills")}
	if home := resolveUserHome(); home != "" {
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

func resolveUserHome() string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Clean(home)
}

func resolveCWD(value string) (string, error) {
	candidate := value
	if !filepath.IsAbs(candidate) {
		base, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
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
