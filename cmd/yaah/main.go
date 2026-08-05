package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"yaah/agent"
	openaiadapter "yaah/provider/openai"
	"yaah/terminal"
	"yaah/tool"
)

const (
	exitSuccess     = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130
)

type providerFactory func(string) (agent.Provider, error)

type appRuntime struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	getenv      func(string) string
	environ     func() []string
	getwd       func() (string, error)
	interrupts  <-chan os.Signal
	newProvider providerFactory
}

func main() {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	code := run(os.Args[1:], appRuntime{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		getenv:     os.Getenv,
		environ:    os.Environ,
		getwd:      os.Getwd,
		interrupts: interrupts,
		newProvider: func(apiKey string) (agent.Provider, error) {
			return openaiadapter.New(apiKey, openaiadapter.Options{})
		},
	})
	signal.Stop(interrupts)
	if code != exitSuccess {
		os.Exit(code)
	}
}

func run(arguments []string, runtime appRuntime) int {
	if err := validateRuntime(runtime); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitFailure
	}

	modelDefault := runtime.getenv("OPENAI_MODEL")
	flags := flag.NewFlagSet("yaah", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	model := flags.String("model", modelDefault, "OpenAI model (or OPENAI_MODEL)")
	cwdFlag := flags.String("cwd", "", "fixed working directory")
	flags.Usage = func() { writeUsage(runtime.stdout) }
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		writeCLIError(runtime.stderr, runtime.getenv("OPENAI_API_KEY"), "usage error: %v", err)
		writeCLIError(runtime.stderr, "", "Run 'yaah --help' for usage.")
		return exitUsage
	}
	if flags.NArg() > 1 {
		writeCLIError(runtime.stderr, runtime.getenv("OPENAI_API_KEY"), "usage error: expected at most one prompt argument")
		writeCLIError(runtime.stderr, "", "Run 'yaah --help' for usage.")
		return exitUsage
	}

	apiKey := runtime.getenv("OPENAI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		writeCLIError(runtime.stderr, "", "OPENAI_API_KEY is required")
		return exitFailure
	}
	if err := validateModel(*model); err != nil {
		writeCLIError(runtime.stderr, apiKey, "%v", err)
		return exitFailure
	}
	cwd, err := resolveCWD(*cwdFlag, runtime.getwd)
	if err != nil {
		writeCLIError(runtime.stderr, apiKey, "%v", err)
		return exitFailure
	}

	prompt := ""
	oneShot := flags.NArg() == 1
	if oneShot {
		prompt = flags.Arg(0)
		if strings.TrimSpace(prompt) == "" {
			writeCLIError(runtime.stderr, apiKey, "one-shot prompt must be nonempty")
			return exitUsage
		}
	}

	registry, err := buildTools(cwd, environmentWithout(runtime.environ(), "OPENAI_API_KEY"))
	if err != nil {
		writeCLIError(runtime.stderr, apiKey, "configure tools: %v", err)
		return exitFailure
	}
	provider, err := runtime.newProvider(apiKey)
	if err != nil {
		writeCLIError(runtime.stderr, apiKey, "configure provider: %v", err)
		return exitFailure
	}
	engine, err := agent.New(provider, registry, agent.Options{Model: *model})
	if err != nil {
		writeCLIError(runtime.stderr, apiKey, "configure agent: %v", err)
		return exitFailure
	}

	terminalOptions := terminal.Options{
		Input:       runtime.stdin,
		Output:      runtime.stdout,
		ErrorOutput: runtime.stderr,
		Model:       *model,
		CWD:         cwd,
		Interrupts:  runtime.interrupts,
		Redact:      []string{apiKey},
	}
	var runErr error
	if oneShot {
		runErr = terminal.RunOneShot(context.Background(), engine, prompt, terminalOptions)
	} else {
		runErr = terminal.Run(context.Background(), engine, terminalOptions)
	}
	if runErr == nil {
		return exitSuccess
	}
	if errors.Is(runErr, terminal.ErrInterrupted) || errors.Is(runErr, context.Canceled) {
		return exitInterrupted
	}
	writeCLIError(runtime.stderr, apiKey, "%v", runErr)
	return exitFailure
}

func validateRuntime(runtime appRuntime) error {
	if runtime.stdin == nil || runtime.stdout == nil || runtime.stderr == nil {
		return errors.New("application streams are required")
	}
	if runtime.getenv == nil || runtime.environ == nil || runtime.getwd == nil || runtime.newProvider == nil {
		return errors.New("application dependencies are required")
	}
	return nil
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
		if candidate == "" {
			candidate = base
		} else {
			candidate = filepath.Join(base, candidate)
		}
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

func buildTools(cwd string, environment []string) (*tool.Registry, error) {
	readTool, err := tool.NewRead(cwd)
	if err != nil {
		return nil, err
	}
	writeTool, err := tool.NewWrite(cwd)
	if err != nil {
		return nil, err
	}
	editTool, err := tool.NewEdit(cwd)
	if err != nil {
		return nil, err
	}
	bashTool, err := tool.NewBash(cwd, tool.BashOptions{Env: environment})
	if err != nil {
		return nil, err
	}
	return tool.NewRegistry(readTool, writeTool, editTool, bashTool)
}

func environmentWithout(environment []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: yaah [--model model] [--cwd directory] [prompt]")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "Configuration:")
	fmt.Fprintln(output, "  OPENAI_API_KEY  required")
	fmt.Fprintln(output, "  OPENAI_MODEL    used when --model is omitted")
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "Commands: /help, /clear, /exit")
}

func writeCLIError(output io.Writer, secret, format string, arguments ...any) {
	message := fmt.Sprintf(format, arguments...)
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	message = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 1000 {
		end := 997
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end] + "..."
	}
	fmt.Fprintf(output, "error: %s\n", message)
}
