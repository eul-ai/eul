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

type fakeAuthenticator struct {
	loginErr        error
	logoutErr       error
	loginDevice     bool
	logoutCalls     int
	interactionCall bool
}

func (authenticator *fakeAuthenticator) LoginBrowser(_ context.Context, presentURL func(string) error) error {
	authenticator.loginDevice = false
	authenticator.interactionCall = true
	_ = presentURL("https://example.test/authorize")
	return authenticator.loginErr
}

func (authenticator *fakeAuthenticator) LoginDevice(_ context.Context, presentCode func(backend.DeviceCode) error) error {
	authenticator.loginDevice = true
	authenticator.interactionCall = true
	_ = presentCode(backend.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "ABCD-EFGH"})
	return authenticator.loginErr
}

func (authenticator *fakeAuthenticator) Logout(context.Context) error {
	authenticator.logoutCalls++
	return authenticator.logoutErr
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
			if test.missingAuth {
				runtime.backends[0].Open = func(context.Context, string) (*backend.Runtime, error) {
					return nil, errors.New("not logged in; run 'eul login'")
				}
			}

			code := run(test.arguments, runtime)
			if code != test.wantCode {
				t.Fatalf("run() code=%d stdout=%q stderr=%q, want code=%d", code, stdout.String(), stderr.String(), test.wantCode)
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
	backendRuntime := &backend.Runtime{Catalog: map[string]backend.ModelMetadata{
		"gpt-5.6-sol":   {ThinkingLevels: agent.ThinkingLevels()},
		"gpt-5.6-luna":  {ThinkingLevels: agent.ThinkingLevels()},
		"gpt-5.6-terra": {ThinkingLevels: agent.ThinkingLevels()},
	}}
	authenticator := &fakeAuthenticator{}
	selected := backend.Backend{
		ID:     "test",
		Name:   "Test Provider",
		Models: backend.ModelDefaults{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"},
		Open: func(context.Context, string) (*backend.Runtime, error) {
			return backendRuntime, nil
		},
		Auth: func(string) backend.Authenticator { return authenticator },
	}

	return appRuntime{
		stdin:         strings.NewReader("/exit\n"),
		stdout:        stdout,
		stderr:        stderr,
		getenv:        func(key string) string { return values[key] },
		getwd:         func() (string, error) { return cwd, nil },
		userHomeDir:   func() (string, error) { return filepath.Join(cwd, ".test-home"), nil },
		userConfigDir: func() (string, error) { return filepath.Join(cwd, ".test-config"), nil },
		backends:      backend.Registry{selected},
		openURL:       func(string) error { return nil },
	}
}

func testAuthenticator(t *testing.T, runtime appRuntime) *fakeAuthenticator {
	t.Helper()
	authenticator, ok := runtime.backends[0].Auth("").(*fakeAuthenticator)
	if !ok {
		t.Fatal("test backend has no fake authenticator")
	}
	return authenticator
}
