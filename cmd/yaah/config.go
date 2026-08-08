package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"yaah/agent"
	openaiadapter "yaah/provider/openai"
	"yaah/terminal"
)

const maxPipedPromptBytes = 1024 * 1024

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
	prompt        string
	oneShot       bool
}

type agentConfig struct {
	model               string
	thinkingLevel       agent.ThinkingLevel
	cwd                 string
	projectInstructions string
	skills              []agent.Skill
	prompt              string
	oneShot             bool
}

func parseAgentArguments(arguments []string, runtime appRuntime) (agentArguments, error) {
	thinkingDefault := runtime.getenv("YAAH_THINKING_LEVEL")
	if thinkingDefault == "" {
		thinkingDefault = string(agent.DefaultThinkingLevel)
	}

	flags := flag.NewFlagSet("yaah", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	model := flags.String("model", runtime.getenv("OPENAI_MODEL"), "OpenAI model (or OPENAI_MODEL)")
	thinking := flags.String("thinking", thinkingDefault, "thinking level (or YAAH_THINKING_LEVEL)")
	cwd := flags.String("cwd", "", "fixed working directory")

	if err := flags.Parse(arguments); err != nil {
		return agentArguments{}, reportedFlagError{error: err}
	}
	if flags.NArg() > 1 {
		return agentArguments{}, errors.New("usage error: expected at most one prompt argument")
	}

	thinkingLevel, err := agent.ParseThinkingLevel(*thinking)
	if err != nil {
		return agentArguments{}, err
	}

	result := agentArguments{model: *model, thinkingLevel: thinkingLevel, cwd: *cwd}
	switch {
	case flags.NArg() == 1:
		result.prompt = flags.Arg(0)
		result.oneShot = true
		if strings.TrimSpace(result.prompt) == "" {
			return agentArguments{}, errors.New("one-shot prompt must be nonempty")
		}
	case !terminal.IsTerminal(runtime.stdin):
		result.prompt, err = readPipedPrompt(runtime.stdin)
		if err != nil {
			return agentArguments{}, err
		}
		result.oneShot = true
	}

	return result, nil
}

func resolveAgentConfig(arguments agentArguments, runtime appRuntime) (agentConfig, error) {
	if err := validateModel(arguments.model); err != nil {
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

	return agentConfig{
		model:               arguments.model,
		thinkingLevel:       arguments.thinkingLevel,
		cwd:                 cwd,
		projectInstructions: projectInstructions,
		skills:              agent.LoadSkills(skillDirectories...),
		prompt:              arguments.prompt,
		oneShot:             arguments.oneShot,
	}, nil
}

func readPipedPrompt(reader io.Reader) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxPipedPromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("read piped prompt: %w", err)
	}
	if len(content) > maxPipedPromptBytes {
		return "", fmt.Errorf("piped prompt exceeds %d bytes", maxPipedPromptBytes)
	}
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		return "", errors.New("piped prompt must be valid UTF-8 text without NUL")
	}

	prompt := string(content)
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("piped prompt must be nonempty")
	}
	return prompt, nil
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
