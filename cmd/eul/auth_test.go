package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type providerOnlyRuntime struct {
	closeCalls int
}

func (*providerOnlyRuntime) NewProvider() (agent.Provider, error) {
	return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}), nil
}

func (runtime *providerOnlyRuntime) Close() error {
	runtime.closeCalls++
	return nil
}

type providerOnlyDriver struct {
	runtime *providerOnlyRuntime
}

func (*providerOnlyDriver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: "provider-only", Name: "Provider Only"}
}

func (driver *providerOnlyDriver) Open(backend.Options) (backend.Runtime, error) {
	return driver.runtime, nil
}

func TestBackendAuthenticationCapabilitiesAreOptional(t *testing.T) {
	driver := &providerOnlyDriver{runtime: &providerOnlyRuntime{}}
	registry, err := backend.NewRegistry("provider-only", driver)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)
	runtime.backends = registry
	for index, command := range []string{"login", "logout"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command}, runtime); code != exitFailure || !strings.Contains(stderr.String(), "does not support login or logout") || driver.runtime.closeCalls != index+1 {
			t.Fatalf("command=%q code=%d close calls=%d stdout=%q stderr=%q", command, code, driver.runtime.closeCalls, stdout.String(), stderr.String())
		}
	}
}

func TestRunLoginAndLogoutCommands(t *testing.T) {
	cwd := t.TempDir()
	for _, test := range []struct {
		name       string
		arguments  []string
		wantDevice bool
		wantText   string
	}{
		{name: "browser", arguments: []string{"login"}, wantText: "Open this URL"},
		{name: "device", arguments: []string{"login", "--device-auth"}, wantDevice: true, wantText: "ABCD-EFGH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, nil)
			driver := testBackendDriver(t, runtime)

			if code := run(test.arguments, runtime); code != exitSuccess || driver.runtime.loginDevice != test.wantDevice || !driver.runtime.interactionCall || driver.runtime.closeCalls != 1 || !strings.Contains(stderr.String(), test.wantText) || stdout.String() != "Logged in with Test Provider.\n" {
				t.Fatalf("code=%d device=%v close calls=%d stdout=%q stderr=%q", code, driver.runtime.loginDevice, driver.runtime.closeCalls, stdout.String(), stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	driver := testBackendDriver(t, runtime)
	if code := run([]string{"logout"}, runtime); code != exitSuccess || driver.runtime.logoutCalls != 1 || driver.runtime.closeCalls != 1 || stdout.String() != "Logged out.\n" {
		t.Fatalf("code=%d calls=%d close calls=%d stdout=%q stderr=%q", code, driver.runtime.logoutCalls, driver.runtime.closeCalls, stdout.String(), stderr.String())
	}

	for _, arguments := range [][]string{{"login", "--help"}, {"logout", "--help"}} {
		stdout.Reset()
		stderr.Reset()
		if code := run(arguments, runtime); code != exitSuccess || !strings.Contains(stderr.String(), "Usage of eul ") {
			t.Fatalf("arguments=%v code=%d stdout=%q stderr=%q", arguments, code, stdout.String(), stderr.String())
		}
	}
}
