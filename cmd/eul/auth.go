package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/eul-ai/eul/backend"
)

type authAction func(context.Context, backend.Backend, backend.Authenticator) error

func runLogin(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul login", flag.ContinueOnError)
	device := flags.Bool("device-auth", false, "use device authorization for headless environments")
	return withAuthenticator(flags, arguments, runtime, func(ctx context.Context, selected backend.Backend, authenticator backend.Authenticator) error {
		var err error
		switch {
		case *device:
			err = authenticator.LoginDevice(ctx, func(code backend.DeviceCode) error {
				fmt.Fprintf(runtime.stderr, "Open %s and enter code: %s\n", code.VerificationURL, code.UserCode)
				return nil
			})
		default:
			err = authenticator.LoginBrowser(ctx, func(url string) error {
				fmt.Fprintf(runtime.stderr, "Open this URL to sign in with %s:\n%s\n", selected.Name, url)
				if runtime.openURL(url) != nil {
					fmt.Fprintln(runtime.stderr, "Browser could not be opened automatically; open the URL manually.")
				}
				return nil
			})
		}
		if err != nil {
			return err
		}

		fmt.Fprintf(runtime.stdout, "Logged in with %s.\n", selected.Name)
		return nil
	})
}

func runLogout(arguments []string, runtime appRuntime) int {
	flags := flag.NewFlagSet("eul logout", flag.ContinueOnError)
	return withAuthenticator(flags, arguments, runtime, func(ctx context.Context, _ backend.Backend, authenticator backend.Authenticator) error {
		if err := authenticator.Logout(ctx); err != nil {
			return err
		}

		fmt.Fprintln(runtime.stdout, "Logged out.")
		return nil
	})
}

func withAuthenticator(flags *flag.FlagSet, arguments []string, runtime appRuntime, action authAction) int {
	flags.SetOutput(runtime.stderr)
	providerID := flags.String("provider", "", "backend")
	switch err := flags.Parse(arguments); {
	case errors.Is(err, flag.ErrHelp):
		return exitSuccess
	case err != nil:
		return exitUsage
	case flags.NArg() != 0:
		writeCLIError(runtime.stderr, "usage error: %s accepts no arguments", flags.Name())
		return exitUsage
	}

	command := strings.TrimPrefix(flags.Name(), "eul ")
	if err := authenticate(command, *providerID, runtime.backends, action); err != nil {
		return finishRun(fmt.Errorf("%s failed: %w", command, err), runtime.stderr)
	}
	return exitSuccess
}

func authenticate(command, providerID string, backends backend.Registry, action authAction) error {
	selected, err := backends.Lookup(providerID)
	if err != nil {
		return err
	}
	if selected.Auth == nil {
		return fmt.Errorf("backend %q does not support %s", selected.ID, command)
	}
	home, err := resolveEULHome()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return action(ctx, selected, selected.Auth(home))
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
