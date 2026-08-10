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

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/builtin"
	"github.com/eul-ai/eul/session"
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

	code := run(os.Args[1:], appRuntime{
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		getenv:        os.Getenv,
		getwd:         os.Getwd,
		userHomeDir:   os.UserHomeDir,
		userConfigDir: os.UserConfigDir,
		interrupts:    interrupts,
		backends:      builtin.New(),
		openURL:       openBrowser,
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

	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "%v", err)
		return exitFailure
	}
	ctx := context.Background()
	runErr := session.Run(ctx, parsed, session.Dependencies{
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
	if isOnlyInterruption(runErr) {
		return exitInterrupted
	}

	writeCLIError(errorOutput, "%v", runErr)
	return exitFailure
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

func runLogin(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul login", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	providerID := flags.String("provider", "", "provider backend")
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

	driver, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}
	authenticator, ok := driver.(backend.Authenticator)
	if !ok {
		writeCLIError(runtime.stderr, "login failed: provider %q does not support login", driver.Descriptor().ID)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	descriptor := driver.Descriptor()
	err = authenticator.Login(ctx, backend.AuthOptions{Home: home, Device: *device}, backend.Interaction{
		OpenURL: func(url string) error {
			fmt.Fprintf(runtime.stderr, "Open this URL to sign in with %s:\n%s\n", descriptor.Name, url)
			if runtime.openURL != nil {
				if err := runtime.openURL(url); err == nil {
					return nil
				}
			}
			fmt.Fprintln(runtime.stderr, "Browser could not be opened automatically; open the URL manually.")
			return nil
		},
		DeviceCode: func(verificationURL, userCode string) error {
			fmt.Fprintf(runtime.stderr, "Open %s and enter code: %s\n", verificationURL, userCode)
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

	fmt.Fprintf(runtime.stdout, "Logged in with %s.\n", descriptor.Name)
	return exitSuccess
}

func runLogout(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul logout", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	providerID := flags.String("provider", "", "provider backend")

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

	driver, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}
	authenticator, ok := driver.(backend.Authenticator)
	if !ok {
		writeCLIError(runtime.stderr, "logout failed: provider %q does not support logout", driver.Descriptor().ID)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	if err := authenticator.Logout(ctx, backend.AuthOptions{Home: home}); err != nil {
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
