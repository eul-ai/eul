package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"yaah/agent"
	oauth "yaah/auth/openai"
	openaiadapter "yaah/provider/openai"
	"yaah/terminal"
	"yaah/tool"
)

const (
	exitSuccess         = 0
	exitFailure         = 1
	exitUsage           = 2
	exitInterrupted     = 130
	maxPipedPromptBytes = 1024 * 1024
)

type providerFactory func(openaiadapter.CodexTokenSource, string) (agent.Provider, error)

type reasoningEffortSetter interface {
	SetReasoningEffort(string) error
}

type oauthManager interface {
	Login(context.Context, oauth.LoginMethod, oauth.Interaction) (oauth.Credentials, error)
	Resolve(context.Context) (oauth.Credentials, error)
	Logout(context.Context) error
}

type appRuntime struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	getenv      func(string) string
	getwd       func() (string, error)
	interrupts  <-chan os.Signal
	newProvider providerFactory
	newOAuth    func() (oauthManager, error)
	openURL     func(string) error
}

func main() {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)

	code := run(os.Args[1:], appRuntime{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		getenv:     os.Getenv,
		getwd:      os.Getwd,
		interrupts: interrupts,
		newOAuth: func() (oauthManager, error) {
			path, err := oauth.DefaultCredentialPath(os.Getenv("YAAH_HOME"))
			if err != nil {
				return nil, err
			}
			return oauth.NewManager(path, oauth.Options{}), nil
		},
		openURL: openBrowser,
		newProvider: func(source openaiadapter.CodexTokenSource, reasoningEffort string) (agent.Provider, error) {
			return openaiadapter.NewCodex(source, openaiadapter.Options{ReasoningEffort: reasoningEffort})
		},
	})

	signal.Stop(interrupts)
	if code != exitSuccess {
		os.Exit(code)
	}
}

func run(arguments []string, runtime appRuntime) int {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "login":
			return runLogin(arguments[1:], runtime)
		case "logout":
			return runLogout(arguments[1:], runtime)
		}
	}

	modelDefault := runtime.getenv("OPENAI_MODEL")
	effortDefault := runtime.getenv("OPENAI_REASONING_EFFORT")
	flags := flag.NewFlagSet("yaah", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	model := flags.String("model", modelDefault, "OpenAI model (or OPENAI_MODEL)")
	effort := flags.String("effort", effortDefault, "reasoning effort (or OPENAI_REASONING_EFFORT)")
	cwdFlag := flags.String("cwd", "", "fixed working directory")

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsage
	}
	if flags.NArg() > 1 {
		writeCLIError(runtime.stderr, "usage error: expected at most one prompt argument")
		return exitUsage
	}

	prompt := ""
	oneShot := flags.NArg() == 1
	if oneShot {
		prompt = flags.Arg(0)
		if strings.TrimSpace(prompt) == "" {
			writeCLIError(runtime.stderr, "one-shot prompt must be nonempty")
			return exitUsage
		}
	} else if !terminal.IsTerminal(runtime.stdin) {
		pipedPrompt, err := readPipedPrompt(runtime.stdin)
		if err != nil {
			writeCLIError(runtime.stderr, "%v", err)
			return exitUsage
		}
		prompt = pipedPrompt
		oneShot = true
	}

	tokenSource, err := resolveTokenSource(runtime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "authentication required: %v", err)
		return exitFailure
	}
	if err := validateModel(*model); err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}
	cwd, err := resolveCWD(*cwdFlag, runtime.getwd)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}

	projectInstructions, err := readProjectInstructions(cwd)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}

	currentEffort := *effort
	provider, err := runtime.newProvider(tokenSource, currentEffort)
	if err != nil {
		writeCLIError(runtime.stderr, "configure provider: %v", err)
		return exitFailure
	}

	var setEffort func(string) error
	if setter, ok := provider.(reasoningEffortSetter); ok {
		setEffort = func(effort string) error {
			if err := setter.SetReasoningEffort(effort); err != nil {
				return err
			}
			currentEffort = effort
			return nil
		}
	}

	subagent := tool.NewSubagent(func(ctx context.Context, task string) (agent.RunResult, error) {
		childProvider, err := runtime.newProvider(tokenSource, currentEffort)
		if err != nil {
			return agent.RunResult{}, fmt.Errorf("configure subagent provider: %w", err)
		}

		childTools := buildSubagentTools(cwd)
		child := agent.New(childProvider, childTools, agent.Options{
			Model:               *model,
			ProjectInstructions: projectInstructions,
		})
		result, runErr := child.Run(ctx, task, func(agent.Event) error { return nil })
		closeErr := childTools.Close()
		if runErr != nil {
			return agent.RunResult{}, runErr
		}
		if closeErr != nil {
			return agent.RunResult{}, fmt.Errorf("close subagent tools: %w", closeErr)
		}
		return result, nil
	})
	registry := buildTools(cwd, subagent)
	engine := agent.New(provider, registry, agent.Options{
		Model:               *model,
		ProjectInstructions: projectInstructions,
	})

	terminalOptions := terminal.Options{
		Input:         runtime.stdin,
		Output:        runtime.stdout,
		ErrorOutput:   runtime.stderr,
		Model:         *model,
		Effort:        currentEffort,
		ContextWindow: openaiadapter.ContextWindow(*model),
		Interrupts:    runtime.interrupts,
		SetEffort:     setEffort,
	}

	if oneShot {
		return finishRun(terminal.RunOneShot(context.Background(), engine, prompt, terminalOptions), runtime.stderr)
	}
	return finishRun(terminal.Run(context.Background(), engine, terminalOptions), runtime.stderr)
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

func finishRun(runErr error, errorOutput io.Writer) int {
	if runErr == nil {
		return exitSuccess
	}
	if errors.Is(runErr, terminal.ErrInterrupted) || errors.Is(runErr, context.Canceled) {
		return exitInterrupted
	}

	writeCLIError(errorOutput, "%v", runErr)
	return exitFailure
}

func resolveTokenSource(runtime appRuntime) (openaiadapter.CodexTokenSource, error) {
	manager, err := runtime.newOAuth()
	if err != nil {
		return nil, err
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()

	if _, err := manager.Resolve(ctx); err != nil {
		return nil, err
	}

	return oauthTokenSource{manager: manager}, nil
}

type oauthTokenSource struct {
	manager oauthManager
}

func (source oauthTokenSource) Token(ctx context.Context) (openaiadapter.CodexCredential, error) {
	credential, err := source.manager.Resolve(ctx)
	if err != nil {
		return openaiadapter.CodexCredential{}, err
	}

	return openaiadapter.CodexCredential{AccessToken: credential.AccessToken, AccountID: credential.AccountID}, nil
}

func runLogin(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("yaah login", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	device := flags.Bool("device-auth", false, "use device authorization for headless environments")

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		writeCLIError(runtime.stderr, "usage error: yaah login accepts no arguments")
		return exitUsage
	}

	method := oauth.LoginBrowser
	if *device {
		method = oauth.LoginDevice
	}

	manager, err := runtime.newOAuth()
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()

	_, err = manager.Login(ctx, method, oauth.Interaction{
		AuthURL: func(url string) error {
			fmt.Fprintf(runtime.stderr, "Open this URL to sign in with ChatGPT:\n%s\n", url)
			if err := runtime.openURL(url); err != nil {
				fmt.Fprintln(runtime.stderr, "Browser could not be opened automatically; open the URL manually.")
			}
			return nil
		},
		DeviceCode: func(code oauth.DeviceCode) error {
			fmt.Fprintf(runtime.stderr, "Open %s and enter code: %s\n", code.VerificationURL, code.UserCode)
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}

	fmt.Fprintln(runtime.stdout, "Logged in with ChatGPT.")
	return exitSuccess
}

func runLogout(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("yaah logout", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		writeCLIError(runtime.stderr, "usage error: yaah logout accepts no arguments")
		return exitUsage
	}

	manager, err := runtime.newOAuth()
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()

	if err := manager.Logout(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}

	fmt.Fprintln(runtime.stdout, "Logged out.")
	return exitSuccess
}

func contextWithInterrupt(interrupts <-chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		select {
		case <-ctx.Done():
		case _, ok := <-interrupts:
			if !ok {
				return
			}
			cancel()
		}
	}()

	return ctx, cancel
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

func buildTools(cwd string, additional ...tool.Tool) *tool.Registry {
	tools := []tool.Tool{
		tool.NewRead(cwd),
		tool.NewWrite(cwd),
		tool.NewEdit(cwd),
		tool.NewBash(cwd),
	}
	tools = append(tools, tool.NewLSP(cwd)...)
	tools = append(tools, additional...)
	return tool.NewRegistry(tools...)
}

func buildSubagentTools(cwd string) *tool.Registry {
	tools := []tool.Tool{tool.NewRead(cwd)}
	for _, lspTool := range tool.NewLSP(cwd) {
		if lspTool.Definition().Name == "lsp_rename" {
			continue
		}
		tools = append(tools, lspTool)
	}
	return tool.NewRegistry(tools...)
}

func writeCLIError(output io.Writer, format string, arguments ...any) {
	fmt.Fprintf(output, "error: "+format+"\n", arguments...)
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}

	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
