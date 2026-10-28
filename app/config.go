package app

import (
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
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

func (settings sessionSettings) stored() session.Settings {
	return session.Settings{
		Provider:         settings.provider,
		WorkingDirectory: settings.cwd,
		Models:           session.Models(settings.models),
		ThinkingLevel:    settings.thinkingLevel,
		FastMode:         settings.fastMode,
	}
}

func storedSessionSettings(settings session.Settings) sessionSettings {
	return sessionSettings{
		provider:      settings.Provider,
		models:        backend.Models(settings.Models),
		thinkingLevel: settings.ThinkingLevel,
		fastMode:      settings.FastMode,
		cwd:           settings.WorkingDirectory,
	}
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

const baseSystemPrompt = `You are a coding agent. Use the available tools to inspect and modify code. Be concise and report results clearly.`

func systemPrompt(config resolvedConfig) string {
	var prompt strings.Builder
	prompt.WriteString(baseSystemPrompt)
	prompt.WriteString("\n\nIndependent tool calls may run concurrently; separate dependent calls.\n")
	if config.projectInstructions != "" {
		prompt.WriteString("\n<project_instructions path=\"")
		prompt.WriteString(html.EscapeString(filepath.ToSlash(filepath.Join(config.cwd, "AGENTS.md"))))
		prompt.WriteString("\">\n")
		prompt.WriteString(strings.TrimSuffix(config.projectInstructions, "\n"))
		prompt.WriteString("\n</project_instructions>\n")
	}
	if section := skill.PromptSection(config.skills); section != "" {
		prompt.WriteString("\n" + section + "\n")
	}
	prompt.WriteString("\nCurrent working directory: ")
	prompt.WriteString(filepath.ToSlash(config.cwd))
	return prompt.String()
}
