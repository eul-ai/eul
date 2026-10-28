package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eul-ai/eul/backend"
)

func TestBackendAuthenticationIsOptional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t, t.TempDir(), &stdout, &stderr)
	runtime.backends = backend.Registry{{ID: "provider-only", Name: "Provider Only"}}
	for _, command := range []string{"login", "logout"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command}, runtime); code != exitFailure {
			t.Fatalf("command=%q code=%d stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
		}
	}
}

func TestRunLoginAndLogoutCommands(t *testing.T) {
	cwd := t.TempDir()
	for _, test := range []struct {
		name       string
		arguments  []string
		wantDevice bool
		wantCode   string
	}{
		{name: "browser", arguments: []string{"login"}},
		{name: "device", arguments: []string{"login", "--device-auth"}, wantDevice: true, wantCode: "ABCD-EFGH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(t, cwd, &stdout, &stderr)
			authenticator := testAuthenticator(t, runtime)

			code := run(test.arguments, runtime)
			if code != exitSuccess || authenticator.loginDevice != test.wantDevice || !authenticator.interactionCall || (test.wantCode != "" && !strings.Contains(stderr.String(), test.wantCode)) {
				t.Fatalf("code=%d device=%v stdout=%q stderr=%q", code, authenticator.loginDevice, stdout.String(), stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t, cwd, &stdout, &stderr)
	authenticator := testAuthenticator(t, runtime)
	if code := run([]string{"logout"}, runtime); code != exitSuccess || authenticator.logoutCalls != 1 {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, authenticator.logoutCalls, stdout.String(), stderr.String())
	}

	for _, arguments := range [][]string{{"login", "--help"}, {"logout", "--help"}} {
		stdout.Reset()
		stderr.Reset()
		if code := run(arguments, runtime); code != exitSuccess {
			t.Fatalf("arguments=%v code=%d stdout=%q stderr=%q", arguments, code, stdout.String(), stderr.String())
		}
	}
}
