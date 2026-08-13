package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex"
	"github.com/eul-ai/eul/interactive"
	"github.com/eul-ai/eul/terminal"
)

const (
	exitSuccess     = 0
	exitFailure     = 1
	exitUsage       = 2
	exitInterrupted = 130
)

type appRuntime struct {
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	getenv        func(string) string
	getwd         func() (string, error)
	userHomeDir   func() (string, error)
	userConfigDir func() (string, error)
	interrupts    <-chan os.Signal
	backends      *backend.Registry
	openURL       func(string) error
}

func main() {
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)

	backends, err := backend.NewRegistry(codex.ID, codex.New())
	if err != nil {
		panic(err)
	}

	code := run(os.Args[1:], appRuntime{
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		getenv:        os.Getenv,
		getwd:         os.Getwd,
		userHomeDir:   os.UserHomeDir,
		userConfigDir: os.UserConfigDir,
		interrupts:    interrupts,
		backends:      backends,
		openURL:       openBrowser,
	})

	signal.Stop(interrupts)
	if code != exitSuccess {
		os.Exit(code)
	}
}

func run(arguments []string, runtime appRuntime) int {
	command := ""
	if len(arguments) > 0 {
		command = arguments[0]
	}

	switch command {
	case "login":
		return runLogin(arguments[1:], runtime)
	case "logout":
		return runLogout(arguments[1:], runtime)
	default:
		return runSession(arguments, runtime)
	}
}

func runSession(arguments []string, runtime appRuntime) int {
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

	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}
	ctx := context.Background()
	runErr := interactive.Run(ctx, parsed, interactive.Dependencies{
		Input:       runtime.stdin,
		Output:      runtime.stdout,
		Home:        home,
		Getwd:       runtime.getwd,
		UserHomeDir: runtime.userHomeDir,
		Interrupts:  runtime.interrupts,
		Backends:    runtime.backends,
	})
	return finishRun(runErr, runtime.stderr)
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

func writeCLIError(output io.Writer, format string, arguments ...any) {
	fmt.Fprintf(output, "error: "+format+"\n", arguments...)
}
