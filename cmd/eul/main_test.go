package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

type fakeBackendRuntime struct {
	checkCredentialsErr   error
	checkCredentialsCalls int
	closeErr              error
	closeCalls            int
	newProvider           func() (agent.Provider, error)
}

func (runtime *fakeBackendRuntime) CheckCredentials(context.Context) error {
	runtime.checkCredentialsCalls++
	return runtime.checkCredentialsErr
}

func (runtime *fakeBackendRuntime) NewProvider() (agent.Provider, error) {
	return runtime.newProvider()
}

func (runtime *fakeBackendRuntime) Close() error {
	runtime.closeCalls++
	return runtime.closeErr
}

type fakeBackendDriver struct {
	descriptor      backend.Descriptor
	defaults        backend.ModelDefaults
	runtime         *fakeBackendRuntime
	openErr         error
	loginErr        error
	logoutErr       error
	loginDevice     bool
	logoutCalls     int
	interactionCall bool
}

func (driver *fakeBackendDriver) Descriptor() backend.Descriptor {
	return driver.descriptor
}

func (driver *fakeBackendDriver) ModelDefaults() backend.ModelDefaults {
	return driver.defaults
}

func (driver *fakeBackendDriver) Open(backend.Options) (backend.Runtime, error) {
	return driver.runtime, driver.openErr
}

func (driver *fakeBackendDriver) Login(_ context.Context, options backend.AuthOptions, interaction backend.Interaction) error {
	driver.loginDevice = options.Device
	if options.Device && interaction.DeviceCode != nil {
		driver.interactionCall = true
		_ = interaction.DeviceCode("https://example.test/device", "ABCD-EFGH")
	}
	if !options.Device && interaction.OpenURL != nil {
		driver.interactionCall = true
		_ = interaction.OpenURL("https://example.test/authorize")
	}
	return driver.loginErr
}

func (driver *fakeBackendDriver) Logout(context.Context, backend.AuthOptions) error {
	driver.logoutCalls++
	return driver.logoutErr
}

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func (providerFunction) ModelMetadata(string) agent.ModelMetadata {
	return agent.ModelMetadata{ThinkingLevels: agent.ThinkingLevels()}
}

func TestFinishRunClassifiesInterruptionAndCleanupFailure(t *testing.T) {
	var output bytes.Buffer
	if code := finishRun(context.Canceled, &output); code != exitInterrupted || output.Len() != 0 {
		t.Fatalf("interruption code=%d output=%q", code, output.String())
	}

	cleanupErr := errors.New("cleanup failed")
	output.Reset()
	if code := finishRun(cleanupErr, &output); code != exitFailure || !strings.Contains(output.String(), cleanupErr.Error()) {
		t.Fatalf("cleanup code=%d output=%q", code, output.String())
	}
}

func TestRunConfigurationAndUsageErrors(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		name        string
		arguments   []string
		missingAuth bool
		wantCode    int
		want        string
	}{
		{name: "help", arguments: []string{"--help"}, wantCode: exitSuccess, want: "Usage of eul:"},
		{name: "missing authentication", missingAuth: true, wantCode: exitFailure, want: "run 'eul login'"},
		{name: "explicit empty model", arguments: []string{"--model="}, wantCode: exitFailure, want: "model is required"},
		{name: "model whitespace", arguments: []string{"--model", "bad model"}, wantCode: exitFailure, want: "must not contain whitespace"},
		{name: "invalid thinking level", arguments: []string{"--thinking", "extreme"}, wantCode: exitUsage, want: "thinking level must be one of"},
		{name: "removed effort flag", arguments: []string{"--effort", "high"}, wantCode: exitUsage, want: "flag provided but not defined"},
		{name: "prompt argument", arguments: []string{"prompt"}, wantCode: exitUsage, want: "accepts no prompt arguments"},
		{name: "bad flag", arguments: []string{"--missing"}, wantCode: exitUsage, want: "flag provided but not defined"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, nil)
			driver := testBackendDriver(t, runtime)
			if test.missingAuth {
				driver.runtime.checkCredentialsErr = errors.New("not logged in; run 'eul login'")
			}

			code := run(test.arguments, runtime)
			combined := stdout.String() + stderr.String()
			if code != test.wantCode || !strings.Contains(combined, test.want) {
				t.Fatalf("run() code=%d stdout=%q stderr=%q, want code=%d containing %q", code, stdout.String(), stderr.String(), test.wantCode, test.want)
			}
			if test.missingAuth && driver.runtime.closeCalls != 1 {
				t.Fatalf("backend close calls = %d, want 1", driver.runtime.closeCalls)
			}
		})
	}
}

func TestRunRejectsInvalidWorkingDirectories(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "file")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"missing", "file"} {
		var stdout, stderr bytes.Buffer
		runtime := testRuntime(cwd, &stdout, &stderr, nil)
		code := run([]string{"--cwd", path}, runtime)
		if code != exitFailure || !strings.Contains(stderr.String(), "working directory") {
			t.Fatalf("path=%q code=%d stderr=%q", path, code, stderr.String())
		}
	}
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer, environment map[string]string) appRuntime {
	values := maps.Clone(environment)
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	driver := &fakeBackendDriver{
		descriptor: backend.Descriptor{ID: "test", Name: "Test Provider"},
		defaults: backend.ModelDefaults{
			Main:     "gpt-5.6-sol",
			Fast:     "gpt-5.6-luna",
			Balanced: "gpt-5.6-terra",
		},
		runtime: backendRuntime,
	}
	backends, err := backend.NewRegistry("test", driver)
	if err != nil {
		panic(err)
	}

	return appRuntime{
		stdin:         strings.NewReader("/exit\n"),
		stdout:        stdout,
		stderr:        stderr,
		getenv:        func(key string) string { return values[key] },
		getwd:         func() (string, error) { return cwd, nil },
		userHomeDir:   func() (string, error) { return filepath.Join(cwd, ".test-home"), nil },
		userConfigDir: func() (string, error) { return filepath.Join(cwd, ".test-config"), nil },
		backends:      backends,
		openURL:       func(string) error { return nil },
	}
}

func testBackendDriver(t *testing.T, runtime appRuntime) *fakeBackendDriver {
	t.Helper()
	driver, err := runtime.backends.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	fake, ok := driver.(*fakeBackendDriver)
	if !ok {
		t.Fatalf("backend driver = %T", driver)
	}
	return fake
}
