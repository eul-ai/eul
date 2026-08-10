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
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
	oauth "github.com/eul-ai/eul/auth/openai"
	openaiadapter "github.com/eul-ai/eul/provider/openai"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

type fakeOAuthManager struct {
	credential      oauth.AccessCredential
	resolveErr      error
	loginErr        error
	logoutErr       error
	loginMethod     oauth.LoginMethod
	logoutCalls     int
	interactionCall bool
	resolveCalls    int
}

func (manager *fakeOAuthManager) Login(_ context.Context, method oauth.LoginMethod, interaction oauth.Interaction) error {
	manager.loginMethod = method
	if method == oauth.LoginDevice && interaction.DeviceCode != nil {
		manager.interactionCall = true
		_ = interaction.DeviceCode(oauth.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "ABCD-EFGH"})
	}
	if method == oauth.LoginBrowser && interaction.AuthURL != nil {
		manager.interactionCall = true
		_ = interaction.AuthURL("https://example.test/authorize")
	}

	return manager.loginErr
}

func (manager *fakeOAuthManager) Resolve(context.Context) (oauth.AccessCredential, error) {
	manager.resolveCalls++
	return manager.credential, manager.resolveErr
}

func (manager *fakeOAuthManager) Logout(context.Context) error {
	manager.logoutCalls++
	return manager.logoutErr
}

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func TestBuildToolsWithoutLSPConfig(t *testing.T) {
	cwd := t.TempDir()

	registry, err := buildToolsetWithHome(cwd, t.TempDir(), fullToolAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	names := make([]string, len(registry.Definitions()))
	for index, definition := range registry.Definitions() {
		names[index] = definition.Name
	}
	want := []string{"bash", "edit", "read", "write"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestBuildSubagentToolsUsesReadOnlyLSP(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	cwd := t.TempDir()
	writeMainTestLSPConfig(t, cwd)

	registry, err := buildToolset(cwd, readOnlyToolAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	names := make([]string, len(registry.Definitions()))
	for index, definition := range registry.Definitions() {
		names[index] = definition.Name
	}
	want := []string{"lsp_definition", "lsp_diagnostics", "lsp_hover", "lsp_references", "lsp_symbols", "read"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestAgentSessionWiresModelAndTools(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Run focused tests before finishing."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	var gotRequest agent.Request
	factoryCalls := 0
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{
		"OPENAI_MODEL":             "environment-model",
		"OPENAI_REASONING_SUMMARY": "detailed",
		"EUL_THINKING_LEVEL":       "high",
	})
	var providerOptions openaiadapter.Options
	runtime.newProvider = func(source openaiadapter.CodexTokenSource, options openaiadapter.Options) (agent.Provider, error) {
		providerOptions = options
		factoryCalls++
		if source == nil {
			t.Fatal("provider token source is nil")
		}
		return providerFunction(func(_ context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
			gotRequest = request
			if err := sink("answer"); err != nil {
				return agent.Response{}, err
			}
			return agent.Response{Text: "answer"}, nil
		}), nil
	}

	arguments, err := parseAgentArguments([]string{"--model", "gpt-5.6-sol", "--thinking", "xhigh"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolveAgentConfig(arguments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	options, err := openAIOptionsFromEnvironment(runtime.getenv)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, oauthTokenSource{manager: &fakeOAuthManager{}}, options)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "test prompt", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}

	if factoryCalls != 1 || providerOptions.ReasoningSummary != openaiadapter.ReasoningSummaryDetailed || gotRequest.Model != "gpt-5.6-sol" || gotRequest.ThinkingLevel != agent.ThinkingXHigh || len(gotRequest.Inputs) != 1 || gotRequest.Inputs[0].Text != "test prompt" {
		t.Fatalf("factory calls=%d request=%+v", factoryCalls, gotRequest)
	}
	if result.Text != "answer" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(gotRequest.Instructions, projectInstructions) || !strings.Contains(gotRequest.Instructions, filepath.ToSlash(filepath.Join(cwd, "AGENTS.md"))) || !strings.Contains(gotRequest.Instructions, "Current working directory: "+filepath.ToSlash(cwd)) {
		t.Fatalf("instructions omit project context:\n%s", gotRequest.Instructions)
	}
	names := make([]string, len(gotRequest.Tools))
	for i, definition := range gotRequest.Tools {
		names[i] = definition.Name
	}
	wantNames := []string{"bash", "edit", "read", "subagent", "update_goal", "write"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
}

func TestAgentSessionFeedsConcurrentSubagentsBackToMain(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Follow project instructions."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{
		"OPENAI_MODEL":             "model",
		"OPENAI_REASONING_SUMMARY": "detailed",
		"EUL_THINKING_LEVEL":       "high",
	})
	var mu sync.Mutex
	factoryCalls := 0
	var childRequests []agent.Request
	mainCalls := 0
	runtime.newProvider = func(_ openaiadapter.CodexTokenSource, options openaiadapter.Options) (agent.Provider, error) {
		if options.ReasoningSummary != openaiadapter.ReasoningSummaryDetailed {
			return nil, errors.New("provider did not receive detailed reasoning summary")
		}
		mu.Lock()
		factoryCalls++
		call := factoryCalls
		mu.Unlock()

		if call == 1 {
			return providerFunction(func(_ context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
				mainCalls++
				switch mainCalls {
				case 1:
					if request.ThinkingLevel != agent.ThinkingHigh {
						t.Fatalf("main thinking level = %q", request.ThinkingLevel)
					}
					foundSubagent := false
					for _, definition := range request.Tools {
						if definition.Name != "subagent" {
							continue
						}
						foundSubagent = true
						if !strings.Contains(definition.Description, "explicitly asks") {
							t.Fatalf("subagent description = %q", definition.Description)
						}
					}
					if !foundSubagent || !strings.Contains(request.Instructions, "explicitly asks for subagents") {
						t.Fatalf("main request omits explicit subagent rule: %+v", request)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "call-1",
						Name:      "subagent",
						Arguments: []byte(`{"tasks":["review alpha","review beta"]}`),
					}}}, nil
				case 2:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent" {
						t.Fatalf("main continuation inputs = %+v", request.Inputs)
					}
					output := request.Inputs[0].Text
					if !strings.Contains(output, "Subagent 1:\nfinding for review alpha") || !strings.Contains(output, "Subagent 2:\nfinding for review beta") {
						t.Fatalf("subagent output = %q", output)
					}
					if err := sink("combined answer"); err != nil {
						return agent.Response{}, err
					}
					return agent.Response{Text: "combined answer"}, nil
				default:
					t.Fatalf("unexpected main provider call %d", mainCalls)
					return agent.Response{}, nil
				}
			}), nil
		}

		return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			mu.Lock()
			childRequests = append(childRequests, request)
			mu.Unlock()
			if len(request.Inputs) != 1 {
				t.Fatalf("child inputs = %+v", request.Inputs)
			}
			return agent.Response{Text: "finding for " + request.Inputs[0].Text}, nil
		}), nil
	}

	arguments, err := parseAgentArguments(nil, runtime)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolveAgentConfig(arguments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	options, err := openAIOptionsFromEnvironment(runtime.getenv)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, oauthTokenSource{manager: &fakeOAuthManager{}}, options)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "use two subagents", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}
	if result.Text != "combined answer" || mainCalls != 2 {
		t.Fatalf("result = %+v, main calls = %d", result, mainCalls)
	}

	mu.Lock()
	defer mu.Unlock()
	if factoryCalls != 3 || len(childRequests) != 2 {
		t.Fatalf("factory calls = %d, child requests = %d", factoryCalls, len(childRequests))
	}
	var tasks []string
	for _, request := range childRequests {
		if request.Model != "model" || request.ThinkingLevel != agent.ThinkingHigh || !strings.Contains(request.Instructions, projectInstructions) || !strings.Contains(request.Instructions, "Current working directory: "+filepath.ToSlash(cwd)) {
			t.Fatalf("child request = %+v", request)
		}
		names := make([]string, len(request.Tools))
		for index, definition := range request.Tools {
			names[index] = definition.Name
		}
		wantNames := []string{"read"}
		if !slices.Equal(names, wantNames) {
			t.Fatalf("child tools = %v, want %v", names, wantNames)
		}
		tasks = append(tasks, request.Inputs[0].Text)
	}
	slices.Sort(tasks)
	if !slices.Equal(tasks, []string{"review alpha", "review beta"}) {
		t.Fatalf("child tasks = %v", tasks)
	}
}

func TestAgentSessionUsesStoredOAuthAtRequestTime(t *testing.T) {
	cwd := t.TempDir()
	const access = "oauth-access-secret"
	manager := &fakeOAuthManager{credential: oauth.AccessCredential{AccessToken: access, AccountID: "account"}}
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{"OPENAI_MODEL": "subscription-model"})
	runtime.newOAuth = fixedOAuth(manager)
	runtime.newProvider = func(source openaiadapter.CodexTokenSource, _ openaiadapter.Options) (agent.Provider, error) {
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

	tokenSource, err := resolveTokenSource(runtime)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := parseAgentArguments(nil, runtime)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolveAgentConfig(arguments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	options, err := openAIOptionsFromEnvironment(runtime.getenv)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, tokenSource, options)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "hello", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}
	if result.Text != "oauth answer" || manager.resolveCalls != 2 {
		t.Fatalf("result=%+v resolveCalls=%d", result, manager.resolveCalls)
	}
}

func TestOnlySessionRequestsRejectCleanupFailures(t *testing.T) {
	newRequest := &terminal.NewSessionRequest{}
	if !onlyNewSessionRequest(errors.Join(newRequest)) {
		t.Fatal("new session request was not recognized")
	}
	if onlyNewSessionRequest(errors.Join(newRequest, errors.New("cleanup failed"))) {
		t.Fatal("new session request hid cleanup failure")
	}

	request := &terminal.ResumeRequest{SessionID: "session"}
	got, ok := onlyResumeRequest(errors.Join(request))
	if !ok || got.SessionID != "session" {
		t.Fatalf("request=%+v ok=%v", got, ok)
	}
	if _, ok := onlyResumeRequest(errors.Join(request, errors.New("cleanup failed"))); ok {
		t.Fatal("resume request hid cleanup failure")
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
			manager := &fakeOAuthManager{credential: oauth.AccessCredential{AccessToken: "access-secret"}}
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
		if code := run(arguments, runtime); code != exitSuccess || !strings.Contains(stderr.String(), "Usage of eul ") {
			t.Fatalf("arguments=%v code=%d stdout=%q stderr=%q", arguments, code, stdout.String(), stderr.String())
		}
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
		{name: "help", arguments: []string{"--help"}, wantCode: exitSuccess, want: "Usage of eul:"},
		{name: "missing authentication", environment: map[string]string{"OPENAI_MODEL": "model"}, missingAuth: true, wantCode: exitFailure, want: "run 'eul login'"},
		{name: "missing model", wantCode: exitFailure, want: "model is required"},
		{name: "explicit empty model", arguments: []string{"--model="}, environment: map[string]string{"OPENAI_MODEL": "fallback"}, wantCode: exitFailure, want: "model is required"},
		{name: "model whitespace", arguments: []string{"--model", "bad model"}, wantCode: exitFailure, want: "must not contain whitespace"},
		{name: "invalid thinking level", arguments: []string{"--thinking", "extreme"}, environment: map[string]string{"OPENAI_MODEL": "model"}, wantCode: exitUsage, want: "thinking level must be one of"},
		{name: "invalid reasoning summary", environment: map[string]string{"OPENAI_MODEL": "model", "OPENAI_REASONING_SUMMARY": "verbose"}, wantCode: exitUsage, want: "OPENAI_REASONING_SUMMARY"},
		{name: "removed effort flag", arguments: []string{"--effort", "high"}, environment: map[string]string{"OPENAI_MODEL": "model"}, wantCode: exitUsage, want: "flag provided but not defined"},
		{name: "prompt argument", arguments: []string{"prompt"}, environment: map[string]string{"OPENAI_MODEL": "model"}, wantCode: exitUsage, want: "accepts no prompt arguments"},
		{name: "bad flag", arguments: []string{"--missing"}, wantCode: exitUsage, want: "flag provided but not defined"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, test.environment)
			if test.missingAuth {
				runtime.newOAuth = fixedOAuth(&fakeOAuthManager{resolveErr: errors.New("oauth: not logged in; run 'eul login'")})
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

func fixedOAuth(manager oauthManager) func() (oauthManager, error) {
	return func() (oauthManager, error) { return manager, nil }
}

func writeMainTestLSPConfig(t *testing.T, cwd string) {
	t.Helper()
	content := `[{"name":"gopls","command":"gopls","languageID":"go","extensions":[".go"]}]`
	if err := os.WriteFile(filepath.Join(cwd, "lsp.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer, environment map[string]string) appRuntime {
	values := maps.Clone(environment)

	return appRuntime{
		stdin:         strings.NewReader("/exit\n"),
		stdout:        stdout,
		stderr:        stderr,
		getenv:        func(key string) string { return values[key] },
		getwd:         func() (string, error) { return cwd, nil },
		userHomeDir:   func() (string, error) { return filepath.Join(cwd, ".test-home"), nil },
		userConfigDir: func() (string, error) { return filepath.Join(cwd, ".test-config"), nil },
		newProvider: func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error) {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		},
		newToolset: func(cwd string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
			tools := []tool.Tool{tool.NewRead(cwd)}
			if access == fullToolAccess {
				tools = append(tools, tool.NewWrite(cwd), tool.NewEdit(cwd), tool.NewBash(cwd))
			}
			tools = append(tools, additional...)
			return tool.NewRegistry(tools)
		},
		newOAuth: fixedOAuth(&fakeOAuthManager{credential: oauth.AccessCredential{AccessToken: "access", AccountID: "account"}}),
		openURL:  func(string) error { return nil },
	}
}
