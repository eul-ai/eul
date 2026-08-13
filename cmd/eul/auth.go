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

	driver, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}
	authenticator, backendRuntime, err := openAuthenticator(driver, home)
	if err != nil {
		writeCLIError(runtime.stderr, "login failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	descriptor := driver.Descriptor()
	method := backend.LoginBrowser
	if *device {
		method = backend.LoginDevice
	}
	err = authenticator.Login(ctx, method, backend.LoginInteraction{
		AuthURL: func(url string) error {
			fmt.Fprintf(runtime.stderr, "Open this URL to sign in with %s:\n%s\n", descriptor.Name, url)
			if runtime.openURL != nil {
				if err := runtime.openURL(url); err == nil {
					return nil
				}
			}
			fmt.Fprintln(runtime.stderr, "Browser could not be opened automatically; open the URL manually.")
			return nil
		},
		DeviceCode: func(code backend.DeviceCode) error {
			fmt.Fprintf(runtime.stderr, "Open %s and enter code: %s\n", code.VerificationURL, code.UserCode)
			return nil
		},
	})
	err = errors.Join(err, backendRuntime.Close())
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

	driver, err := runtime.backends.Lookup(*providerID)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}
	home, err := resolveEULHome(runtime)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}
	authenticator, backendRuntime, err := openAuthenticator(driver, home)
	if err != nil {
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}

	ctx, cancel := contextWithInterrupt(runtime.interrupts)
	defer cancel()
	err = authenticator.Logout(ctx)
	err = errors.Join(err, backendRuntime.Close())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		writeCLIError(runtime.stderr, "logout failed: %v", err)
		return exitFailure
	}

	fmt.Fprintln(runtime.stdout, "Logged out.")
	return exitSuccess
}

func openAuthenticator(driver backend.Driver, home string) (backend.Authenticator, backend.Runtime, error) {
	backendRuntime, err := driver.Open(backend.Options{Home: home})
	if err != nil {
		return nil, nil, err
	}
	authenticator, ok := backendRuntime.(backend.Authenticator)
	if !ok {
		return nil, nil, errors.Join(fmt.Errorf("backend %q does not support login or logout", driver.Descriptor().ID), backendRuntime.Close())
	}
	return authenticator, backendRuntime, nil
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
