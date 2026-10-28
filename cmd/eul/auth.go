package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/eul-ai/eul/backend"
)

func runLogin(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul login", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	providerID := flags.String("provider", "", "backend")
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

	selected, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}
	if selected.Auth == nil {
		writeCLIError(runtime.stderr, "login failed: backend %q does not support login", selected.ID)
		return exitFailure
	}
	authenticator := selected.Auth(home)

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	if *device {
		err = authenticator.LoginDevice(ctx, func(code backend.DeviceCode) error {
			fmt.Fprintf(runtime.stderr, "Open %s and enter code: %s\n", code.VerificationURL, code.UserCode)
			return nil
		})
	} else {
		err = authenticator.LoginBrowser(ctx, func(url string) error {
			fmt.Fprintf(runtime.stderr, "Open this URL to sign in with %s:\n%s\n", selected.Name, url)
			if runtime.openURL != nil {
				if err := runtime.openURL(url); err == nil {
					return nil
				}
			}
			fmt.Fprintln(runtime.stderr, "Browser could not be opened automatically; open the URL manually.")
			return nil
		})
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}

	fmt.Fprintf(runtime.stdout, "Logged in with %s.\n", selected.Name)
	return exitSuccess
}

func runLogout(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul logout", flag.ContinueOnError)
	flags.SetOutput(runtime.stderr)
	providerID := flags.String("provider", "", "backend")

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

	selected, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}
	if selected.Auth == nil {
		writeCLIError(runtime.stderr, "logout failed: backend %q does not support logout", selected.ID)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	if err := selected.Auth(home).Logout(ctx); err != nil {
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
