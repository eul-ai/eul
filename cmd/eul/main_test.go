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
	loginErr              error
	logoutErr             error
	loginDevice           bool
	logoutCalls           int
	interactionCall       bool
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

func (runtime *fakeBackendRuntime) Login(_ context.Context, method backend.LoginMethod, interaction backend.LoginInteraction) error {
	runtime.loginDevice = method == backend.LoginDevice
	if method == backend.LoginDevice && interaction.DeviceCode != nil {
		runtime.interactionCall = true
		_ = interaction.DeviceCode(backend.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "ABCD-EFGH"})
	}
	if method == backend.LoginBrowser && interaction.AuthURL != nil {
		runtime.interactionCall = true
		_ = interaction.AuthURL("https://example.test/authorize")
	}
	return runtime.loginErr
}

func (runtime *fakeBackendRuntime) Logout(context.Context) error {
	runtime.logoutCalls++
	return runtime.logoutErr
}

type fakeBackendDriver struct {
	descriptor backend.Descriptor
	runtime    *fakeBackendRuntime
	openErr    error
}

func (driver *fakeBackendDriver) Descriptor() backend.Descriptor {
	return driver.descriptor
}

func (driver *fakeBackendDriver) Open(backend.Options) (backend.Runtime, error) {
	return driver.runtime, driver.openErr
}

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func (providerFunction) ModelMetadata(string) backend.ModelMetadata {
	return backend.ModelMetadata{ThinkingLevels: agent.ThinkingLevels()}
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
	}{
		{name: "help", arguments: []string{"--help"}, wantCode: exitSuccess},
		{name: "missing authentication", missingAuth: true, wantCode: exitFailure},
		{name: "explicit empty model", arguments: []string{"--model="}, wantCode: exitFailure},
		{name: "model whitespace", arguments: []string{"--model", "bad model"}, wantCode: exitFailure},
		{name: "invalid thinking level", arguments: []string{"--thinking", "extreme"}, wantCode: exitUsage},
		{name: "removed effort flag", arguments: []string{"--effort", "high"}, wantCode: exitUsage},
		{name: "prompt argument", arguments: []string{"prompt"}, wantCode: exitUsage},
		{name: "bad flag", arguments: []string{"--missing"}, wantCode: exitUsage},
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
			if code != test.wantCode {
				t.Fatalf("run() code=%d stdout=%q stderr=%q, want code=%d", code, stdout.String(), stderr.String(), test.wantCode)
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
		if code != exitFailure {
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
		descriptor: backend.Descriptor{ID: "test", Name: "Test Provider", DefaultModels: backend.ModelDefaults{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"}},
		runtime:    backendRuntime,
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
