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
	"runtime"

	"github.com/eul-ai/eul/agent"
	oauth "github.com/eul-ai/eul/auth/openai"
	openaiadapter "github.com/eul-ai/eul/provider/openai"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

const (
	exitSuccess     = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130
)

type providerFactory func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	readOnlyToolAccess
)

type toolsetFactory func(string, toolAccess, ...tool.Tool) (*tool.Registry, error)

type oauthManager interface {
	Login(context.Context, oauth.LoginMethod, oauth.Interaction) error
	Resolve(context.Context) (oauth.AccessCredential, error)
	Logout(context.Context) error
}

type appRuntime struct {
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	getenv        func(string) string
	getwd         func() (string, error)
	userHomeDir   func() (string, error)
	userConfigDir func() (string, error)
	interrupts    <-chan os.Signal
	newProvider   providerFactory
	newToolset    toolsetFactory
	newOAuth      func() (oauthManager, error)
	openURL       func(string) error
}

func main() {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)

	code := run(os.Args[1:], appRuntime{
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		getenv:        os.Getenv,
		getwd:         os.Getwd,
		userHomeDir:   os.UserHomeDir,
		userConfigDir: os.UserConfigDir,
		interrupts:    interrupts,
		newOAuth: func() (oauthManager, error) {
			path, err := oauth.DefaultCredentialPath(os.Getenv("EUL_HOME"))
			if err != nil {
				return nil, err
			}
			return oauth.NewManager(path, oauth.Options{}), nil
		},
		openURL:    openBrowser,
		newToolset: buildToolset,
		newProvider: func(source openaiadapter.CodexTokenSource, options openaiadapter.Options) (agent.Provider, error) {
			return openaiadapter.NewCodex(source, options)
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

	parsed, err := parseAgentArguments(arguments, runtime)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		var reported reportedFlagError
		if !errors.As(err, &reported) {
			writeCLIError(runtime.stderr, "%v", err)
		}
		return exitUsage
	}

	providerOptions, err := openAIOptionsFromEnvironment(runtime.getenv)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitUsage
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}
	store := newSessionStore(home)
	tokenSource, err := resolveTokenSource(runtime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "authentication required: %v", err)
		return exitFailure
	}

	ctx := context.Background()
	config, handle, err := resolveInitialSession(ctx, parsed, runtime, store)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}
	session, err := newStoredAgentSession(config, runtime, tokenSource, providerOptions, store, handle)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}

	runner, err := terminal.NewRunner(runtime.stdin, runtime.stdout)
	if err != nil {
		return finishRun(session.finish(err), runtime.stderr)
	}
	runSessions := func() error {
		for {
			runErr := session.run(ctx, runner)
			if onlyNewSessionRequest(runErr) {
				config, err = resolveAgentConfig(agentArguments{
					model:         config.model,
					thinkingLevel: session.thinkingLevel,
					cwd:           config.cwd,
				}, runtime)
				if err != nil {
					return err
				}
				session, err = newStoredAgentSession(config, runtime, tokenSource, providerOptions, store, nil)
				if err != nil {
					return err
				}
				continue
			}

			request, resume := onlyResumeRequest(runErr)
			if !resume {
				return runErr
			}

			config, handle, err = resolveStoredSession(ctx, store, runtime, config.cwd, request.SessionID)
			if err != nil {
				return err
			}
			session, err = newStoredAgentSession(config, runtime, tokenSource, providerOptions, store, handle)
			if err != nil {
				return err
			}
		}
	}
	runErr := runSessions()
	return finishRun(errors.Join(runErr, runner.Close()), runtime.stderr)
}

func finishRun(runErr error, errorOutput io.Writer) int {
	if runErr == nil {
		return exitSuccess
	}
	if isOnlyInterruption(runErr) {
		return exitInterrupted
	}

	writeCLIError(errorOutput, "%v", runErr)
	return exitFailure
}

func onlyNewSessionRequest(err error) bool {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		_, ok := err.(*terminal.NewSessionRequest)
		return ok
	}

	causes := joined.Unwrap()
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !onlyNewSessionRequest(cause) {
			return false
		}
	}
	return true
}

func onlyResumeRequest(err error) (*terminal.ResumeRequest, bool) {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		request, ok := err.(*terminal.ResumeRequest)
		return request, ok
	}

	causes := joined.Unwrap()
	if len(causes) == 0 {
		return nil, false
	}
	var selected *terminal.ResumeRequest
	for _, cause := range causes {
		request, ok := onlyResumeRequest(cause)
		if !ok || selected != nil && selected.SessionID != request.SessionID {
			return nil, false
		}
		selected = request
	}
	return selected, selected != nil
}

func isOnlyInterruption(err error) bool {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return errors.Is(err, terminal.ErrInterrupted) || errors.Is(err, context.Canceled)
	}

	causes := joined.Unwrap()
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !isOnlyInterruption(cause) {
			return false
		}
	}
	return true
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
	flags := flag.NewFlagSet("eul login", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	device := flags.Bool("device-auth", false, "use device authorization for headless environments")

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		writeCLIError(runtime.stderr, "usage error: eul login accepts no arguments")
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

	err = manager.Login(ctx, method, oauth.Interaction{
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
	flags := flag.NewFlagSet("eul logout", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsage
	}
	if flags.NArg() != 0 {
		writeCLIError(runtime.stderr, "usage error: eul logout accepts no arguments")
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
