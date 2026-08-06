package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yaah/agent"
	oauth "yaah/auth/openai"
	openaiadapter "yaah/provider/openai"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

type fakeOAuthManager struct {
	credential      oauth.Credentials
	resolveErr      error
	loginErr        error
	logoutErr       error
	loginMethod     oauth.LoginMethod
	logoutCalls     int
	interactionCall bool
	resolveCalls    int
}

func (manager *fakeOAuthManager) Login(_ context.Context, method oauth.LoginMethod, interaction oauth.Interaction) (oauth.Credentials, error) {
	manager.loginMethod = method
	if method == oauth.LoginDevice && interaction.DeviceCode != nil {
		manager.interactionCall = true
		_ = interaction.DeviceCode(oauth.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "ABCD-EFGH"})
	}
	if method == oauth.LoginBrowser && interaction.AuthURL != nil {
		manager.interactionCall = true
		_ = interaction.AuthURL("https://example.test/authorize")
	}

	return manager.credential, manager.loginErr
}

func (manager *fakeOAuthManager) Resolve(context.Context) (oauth.Credentials, error) {
	manager.resolveCalls++
	return manager.credential, manager.resolveErr
}

func (manager *fakeOAuthManager) Logout(context.Context) error {
	manager.logoutCalls++
	return manager.logoutErr
}

func (function providerFunction) Generate(ctx context.Context, request agent.Request, sink, _ agent.TextSink) (agent.Response, error) {
	return function(ctx, request, sink)
}

func TestRunOneShotWiresModelToolsAndOutput(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Run focused tests before finishing."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	var gotRequest agent.Request
	factoryEffort := ""
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{
		"OPENAI_MODEL":            "environment-model",
		"OPENAI_REASONING_EFFORT": "high",
	})
	runtime.newProvider = func(source openaiadapter.CodexTokenSource, reasoningEffort string) (agent.Provider, error) {
		if source == nil {
			t.Fatal("provider token source is nil")
		}
		factoryEffort = reasoningEffort
		return providerFunction(func(_ context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
			gotRequest = request
			if err := sink("answer"); err != nil {
				return agent.Response{}, err
			}
			return agent.Response{Text: "answer"}, nil
		}), nil
	}

	code := run([]string{"--model", "flag-model", "--effort", "xhigh", "one shot prompt"}, runtime)
	if code != exitSuccess {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if factoryEffort != "xhigh" || gotRequest.Model != "flag-model" || len(gotRequest.Inputs) != 1 || gotRequest.Inputs[0].Text != "one shot prompt" {
		t.Fatalf("factory effort=%q request=%+v", factoryEffort, gotRequest)
	}
	if !strings.HasSuffix(gotRequest.Instructions, projectInstructions) {
		t.Fatalf("instructions omit AGENTS.md:\n%s", gotRequest.Instructions)
	}
	names := make([]string, len(gotRequest.Tools))
	for i, definition := range gotRequest.Tools {
		names[i] = definition.Name
	}
	if !slices.Equal(names, []string{"bash", "edit", "lsp_definition", "lsp_diagnostics", "lsp_hover", "lsp_references", "lsp_rename", "lsp_symbols", "read", "write"}) {
		t.Fatalf("tools = %v", names)
	}
	if stdout.String() != "answer\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunUsesStoredOAuthAndResolvesTokenAtRequestTime(t *testing.T) {
	cwd := t.TempDir()
	const access = "oauth-access-secret"
	const refresh = "oauth-refresh-secret"
	manager := &fakeOAuthManager{credential: oauth.Credentials{
		Version: 1, Type: "oauth", AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account",
	}}
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{"OPENAI_MODEL": "subscription-model"})
	runtime.newOAuth = fixedOAuth(manager)
	runtime.newProvider = func(source openaiadapter.CodexTokenSource, _ string) (agent.Provider, error) {
		if source == nil {
			t.Fatal("provider token source is nil")
		}
		credential, err := source.Token(context.Background())
		if err != nil {
			return nil, err
		}
		if credential.AccessToken != access || credential.AccountID != "account" {
			t.Fatalf("Codex credential = %+v", credential)
		}
		return providerFunction(func(_ context.Context, _ agent.Request, sink agent.TextSink) (agent.Response, error) {
			if err := sink("oauth answer"); err != nil {
				return agent.Response{}, err
			}
			return agent.Response{Text: "oauth answer"}, nil
		}), nil
	}

	if code := run([]string{"hello"}, runtime); code != exitSuccess {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "oauth answer\n" || strings.Contains(stdout.String()+stderr.String(), access) || strings.Contains(stdout.String()+stderr.String(), refresh) || manager.resolveCalls != 2 {
		t.Fatalf("stdout=%q stderr=%q resolveCalls=%d", stdout.String(), stderr.String(), manager.resolveCalls)
	}
}

func TestRunLoginAndLogoutCommands(t *testing.T) {
	cwd := t.TempDir()
	for _, test := range []struct {
		name       string
		arguments  []string
		wantMethod oauth.LoginMethod
		wantText   string
	}{
		{name: "browser", arguments: []string{"login"}, wantMethod: oauth.LoginBrowser, wantText: "Open this URL"},
		{name: "device", arguments: []string{"login", "--device-auth"}, wantMethod: oauth.LoginDevice, wantText: "ABCD-EFGH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			manager := &fakeOAuthManager{credential: oauth.Credentials{AccessToken: "access-secret", RefreshToken: "refresh-secret"}}
			runtime := testRuntime(cwd, &stdout, &stderr, nil)
			runtime.newOAuth = fixedOAuth(manager)

			if code := run(test.arguments, runtime); code != exitSuccess || manager.loginMethod != test.wantMethod || !manager.interactionCall || !strings.Contains(stderr.String(), test.wantText) || stdout.String() != "Logged in with ChatGPT.\n" {
				t.Fatalf("code=%d method=%q stdout=%q stderr=%q", code, manager.loginMethod, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "access-secret") || strings.Contains(stdout.String()+stderr.String(), "refresh-secret") {
				t.Fatal("OAuth secret leaked")
			}
		})
	}

	var stdout, stderr bytes.Buffer
	manager := &fakeOAuthManager{}
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	runtime.newOAuth = fixedOAuth(manager)
	if code := run([]string{"logout"}, runtime); code != exitSuccess || manager.logoutCalls != 1 || stdout.String() != "Logged out.\n" {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, manager.logoutCalls, stdout.String(), stderr.String())
	}

	for _, arguments := range [][]string{{"login", "--help"}, {"logout", "--help"}} {
		stdout.Reset()
		stderr.Reset()
		if code := run(arguments, runtime); code != exitSuccess || !strings.Contains(stderr.String(), "Usage of yaah ") {
			t.Fatalf("arguments=%v code=%d stdout=%q stderr=%q", arguments, code, stdout.String(), stderr.String())
		}
	}
}

func TestRunInteractiveUsesEnvironmentModelAndResolvedCWD(t *testing.T) {
	cwd := t.TempDir()
	nested := filepath.Join(cwd, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{
		"OPENAI_MODEL": "environment-model",
	})
	runtime.stdin = strings.NewReader("/exit\n")

	code := run([]string{"--cwd", "nested"}, runtime)
	if code != exitSuccess {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "openai/environment-model") || !strings.Contains(stderr.String(), nested) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunConfigurationAndUsageErrors(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		name        string
		arguments   []string
		environment map[string]string
		missingAuth bool
		wantCode    int
		want        string
	}{
		{name: "help", arguments: []string{"--help"}, wantCode: exitSuccess, want: "Usage of yaah:"},
		{name: "missing authentication", environment: map[string]string{"OPENAI_MODEL": "model"}, missingAuth: true, wantCode: exitFailure, want: "run 'yaah login'"},
		{name: "missing model", wantCode: exitFailure, want: "model is required"},
		{name: "explicit empty model", arguments: []string{"--model="}, environment: map[string]string{"OPENAI_MODEL": "fallback"}, wantCode: exitFailure, want: "model is required"},
		{name: "model whitespace", arguments: []string{"--model", "bad model"}, wantCode: exitFailure, want: "must not contain whitespace"},
		{name: "extra prompts", arguments: []string{"one", "two"}, environment: map[string]string{"OPENAI_MODEL": "model"}, wantCode: exitUsage, want: "at most one prompt"},
		{name: "empty prompt", arguments: []string{""}, environment: map[string]string{"OPENAI_MODEL": "model"}, wantCode: exitUsage, want: "prompt must be nonempty"},
		{name: "bad flag", arguments: []string{"--missing"}, wantCode: exitUsage, want: "flag provided but not defined"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, test.environment)
			if test.missingAuth {
				runtime.newOAuth = fixedOAuth(&fakeOAuthManager{resolveErr: errors.New("oauth: not logged in; run 'yaah login'")})
			}

			code := run(test.arguments, runtime)
			combined := stdout.String() + stderr.String()
			if code != test.wantCode || !strings.Contains(combined, test.want) {
				t.Fatalf("run() code=%d stdout=%q stderr=%q, want code=%d containing %q", code, stdout.String(), stderr.String(), test.wantCode, test.want)
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
		runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{"OPENAI_MODEL": "model"})
		code := run([]string{"--cwd", path}, runtime)
		if code != exitFailure || !strings.Contains(stderr.String(), "working directory") {
			t.Fatalf("path=%q code=%d stderr=%q", path, code, stderr.String())
		}
	}
}

func TestRunOneShotInterruptReturns130(t *testing.T) {
	cwd := t.TempDir()
	started := make(chan struct{})
	interrupts := make(chan os.Signal, 1)
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{"OPENAI_MODEL": "model"})
	runtime.interrupts = interrupts
	runtime.newProvider = func(openaiadapter.CodexTokenSource, string) (agent.Provider, error) {
		return providerFunction(func(ctx context.Context, _ agent.Request, _ agent.TextSink) (agent.Response, error) {
			close(started)
			<-ctx.Done()
			return agent.Response{}, ctx.Err()
		}), nil
	}

	done := make(chan int, 1)
	go func() { done <- run([]string{"wait"}, runtime) }()
	select {
	case <-started:
		interrupts <- os.Interrupt
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	select {
	case code := <-done:
		if code != exitInterrupted {
			t.Fatalf("run() code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
}

func fixedOAuth(manager oauthManager) func() (oauthManager, error) {
	return func() (oauthManager, error) { return manager, nil }
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer, environment map[string]string) appRuntime {
	values := maps.Clone(environment)

	return appRuntime{
		stdin:  strings.NewReader("/exit\n"),
		stdout: stdout,
		stderr: stderr,
		getenv: func(key string) string { return values[key] },
		getwd:  func() (string, error) { return cwd, nil },
		newProvider: func(openaiadapter.CodexTokenSource, string) (agent.Provider, error) {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		},
		newOAuth: fixedOAuth(&fakeOAuthManager{credential: oauth.Credentials{AccessToken: "access", AccountID: "account"}}),
		openURL:  func(string) error { return nil },
	}
}
