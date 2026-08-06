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
	"sync"
	"unicode"

	"yaah/agent"
	oauth "yaah/auth/openai"
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

type providerConfig struct {
	apiKey          string
	codexToken      openaiadapter.CodexTokenSource
	reasoningEffort string
}

type providerFactory func(providerConfig) (agent.Provider, error)

type oauthManager interface {
	Login(context.Context, oauth.LoginMethod, oauth.Interaction) (oauth.Credentials, error)
	Resolve(context.Context) (oauth.Credentials, error)
	Logout(context.Context) error
}

type lazyOAuthManager struct {
	yaahHome string
	once     sync.Once
	manager  *oauth.Manager
	err      error
}

func (manager *lazyOAuthManager) get() (*oauth.Manager, error) {
	manager.once.Do(func() {
		var path string
		path, manager.err = oauth.DefaultCredentialPath(manager.yaahHome)
		if manager.err == nil {
			manager.manager, manager.err = oauth.NewManager(path, oauth.Options{})
		}
	})
	return manager.manager, manager.err
}

func (manager *lazyOAuthManager) Login(ctx context.Context, method oauth.LoginMethod, interaction oauth.Interaction) (oauth.Credentials, error) {
	resolved, err := manager.get()
	if err != nil {
		return oauth.Credentials{}, err
	}
	return resolved.Login(ctx, method, interaction)
}

func (manager *lazyOAuthManager) Resolve(ctx context.Context) (oauth.Credentials, error) {
	resolved, err := manager.get()
	if err != nil {
		return oauth.Credentials{}, err
	}
	return resolved.Resolve(ctx)
}

func (manager *lazyOAuthManager) Logout(ctx context.Context) error {
	resolved, err := manager.get()
	if err != nil {
		return err
	}
	return resolved.Logout(ctx)
}

type appRuntime struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	getenv      func(string) string
	environ     func() []string
	getwd       func() (string, error)
	interrupts  <-chan os.Signal
	newProvider providerFactory
	oauth       oauthManager
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
		environ:    os.Environ,
		getwd:      os.Getwd,
		interrupts: interrupts,
		oauth:      &lazyOAuthManager{yaahHome: os.Getenv("YAAH_HOME")},
		openURL:    openBrowser,
		newProvider: func(config providerConfig) (agent.Provider, error) {
			options := openaiadapter.Options{ReasoningEffort: config.reasoningEffort}
			if config.apiKey != "" {
				return openaiadapter.New(config.apiKey, options)
			}
			return openaiadapter.NewCodex(config.codexToken, options)
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

	apiKey := runtime.getenv("OPENAI_API_KEY")
	config := providerConfig{reasoningEffort: *effort}
	if strings.TrimSpace(apiKey) != "" {
		config.apiKey = apiKey
	} else {
		ctx, cancel := contextWithInterrupt(runtime.interrupts)
		_, err := runtime.oauth.Resolve(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return exitInterrupted
			}
			writeCLIError(runtime.stderr, "authentication required: %v", err)
			return exitFailure
		}
		config.codexToken = oauthTokenSource{manager: runtime.oauth}
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

	prompt := ""
	oneShot := flags.NArg() == 1
	if oneShot {
		prompt = flags.Arg(0)
		if strings.TrimSpace(prompt) == "" {
			writeCLIError(runtime.stderr, "one-shot prompt must be nonempty")
			return exitUsage
		}
	}

	registry, err := buildTools(cwd, environmentWithout(runtime.environ(), "OPENAI_API_KEY"))
	if err != nil {
		writeCLIError(runtime.stderr, "configure tools: %v", err)
		return exitFailure
	}
	provider, err := runtime.newProvider(config)
	if err != nil {
		writeCLIError(runtime.stderr, "configure provider: %v", err)
		return exitFailure
	}
	engine, err := agent.New(provider, registry, agent.Options{Model: *model})
	if err != nil {
		writeCLIError(runtime.stderr, "configure agent: %v", err)
		return exitFailure
	}

	terminalOptions := terminal.Options{
		Input:       runtime.stdin,
		Output:      runtime.stdout,
		ErrorOutput: runtime.stderr,
		Model:       *model,
		CWD:         cwd,
		Interrupts:  runtime.interrupts,
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
	writeCLIError(runtime.stderr, "%v", runErr)
	return exitFailure
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
	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	_, err := runtime.oauth.Login(ctx, method, oauth.Interaction{
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
	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	if err := runtime.oauth.Logout(ctx); err != nil {
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
	if interrupts == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-ctx.Done():
		case _, ok := <-interrupts:
			if ok {
				cancel()
			}
		}
	}()
	return ctx, cancel
}

func validateRuntime(runtime appRuntime) error {
	if runtime.stdin == nil || runtime.stdout == nil || runtime.stderr == nil {
		return errors.New("application streams are required")
	}
	if runtime.getenv == nil || runtime.environ == nil || runtime.getwd == nil || runtime.newProvider == nil || runtime.oauth == nil || runtime.openURL == nil {
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
