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
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

type fakeBackendInstance struct {
	checkCredentialsErr   error
	checkCredentialsCalls int
	closeErr              error
	closeCalls            int
	newProvider           func() (agent.Provider, error)
}

func (instance *fakeBackendInstance) CheckCredentials(context.Context) error {
	instance.checkCredentialsCalls++
	return instance.checkCredentialsErr
}

func (instance *fakeBackendInstance) NewProvider() (agent.Provider, error) {
	return instance.newProvider()
}

func (instance *fakeBackendInstance) Close() error {
	instance.closeCalls++
	return instance.closeErr
}

type fakeBackendDriver struct {
	descriptor      backend.Descriptor
	defaults        backend.ModelDefaults
	instance        *fakeBackendInstance
	configureErr    error
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

func (driver *fakeBackendDriver) Configure(backend.Options) (backend.Instance, error) {
	return driver.instance, driver.configureErr
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

type providerOnlyInstance struct {
	closeCalls int
}

func (*providerOnlyInstance) NewProvider() (agent.Provider, error) {
	return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}), nil
}

func (instance *providerOnlyInstance) Close() error {
	instance.closeCalls++
	return nil
}

type providerOnlyDriver struct {
	instance *providerOnlyInstance
}

func (*providerOnlyDriver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: "provider-only", Name: "Provider Only"}
}

func (*providerOnlyDriver) ModelDefaults() backend.ModelDefaults {
	return backend.ModelDefaults{Main: "model"}
}

func (driver *providerOnlyDriver) Configure(backend.Options) (backend.Instance, error) {
	return driver.instance, nil
}

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func (providerFunction) ModelMetadata(string) agent.ModelMetadata {
	return agent.ModelMetadata{ThinkingLevels: agent.ThinkingLevels()}
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
		"EUL_THINKING_LEVEL": "high",
	})
	driver := testBackendDriver(t, runtime)
	driver.instance.newProvider = func() (agent.Provider, error) {
		factoryCalls++
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
	config, err := resolveTestAgentConfig(arguments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, driver.instance)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "test prompt", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}

	if driver.instance.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", driver.instance.closeCalls)
	}
	if factoryCalls != 1 || gotRequest.Model != "gpt-5.6-sol" || gotRequest.ThinkingLevel != agent.ThinkingXHigh || len(gotRequest.Inputs) != 1 || gotRequest.Inputs[0].Text != "test prompt" {
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
	wantNames := []string{"bash", "edit", "read", "subagent", "subagent_cancel", "subagent_wait", "update_goal", "write"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
}

func TestAgentSessionLaunchesAndWaitsForConcurrentSubagents(t *testing.T) {
	cwd := t.TempDir()
	projectInstructions := "Follow project instructions."
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(projectInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, map[string]string{
		"EUL_THINKING_LEVEL": "high",
	})
	var mu sync.Mutex
	factoryCalls := 0
	var childRequests []agent.Request
	mainCalls := 0
	driver := testBackendDriver(t, runtime)
	driver.instance.newProvider = func() (agent.Provider, error) {
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
					definitions := make(map[string]agent.ToolDefinition, len(request.Tools))
					for _, definition := range request.Tools {
						definitions[definition.Name] = definition
					}
					if !strings.Contains(definitions["subagent"].Description, "Use selectively") || definitions["subagent_wait"].Name == "" || definitions["subagent_cancel"].Name == "" {
						t.Fatalf("subagent definitions = %+v", definitions)
					}
					if strings.Contains(request.Instructions, "explicitly asks for subagents") {
						t.Fatalf("main request retains explicit subagent rule: %+v", request)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "launch",
						Name:      "subagent",
						Arguments: []byte(`{"tasks":["review alpha","review beta"]}`),
					}}}, nil
				case 2:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent" {
						t.Fatalf("launch continuation inputs = %+v", request.Inputs)
					}
					output := request.Inputs[0].Text
					if !strings.Contains(output, "Started subagents (model: balanced, thinking: low)") || !strings.Contains(output, "subagent-1: review alpha") || !strings.Contains(output, "subagent-2: review beta") || strings.Contains(output, "finding for") {
						t.Fatalf("launch output = %q", output)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "read",
						Name:      "read",
						Arguments: []byte(`{"path":"AGENTS.md"}`),
					}}}, nil
				case 3:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "read" || !strings.Contains(request.Inputs[0].Text, projectInstructions) {
						t.Fatalf("independent continuation inputs = %+v", request.Inputs)
					}
					return agent.Response{ToolCalls: []agent.ToolCall{{
						ID:        "wait",
						Name:      "subagent_wait",
						Arguments: []byte(`{"ids":["subagent-1","subagent-2"]}`),
					}}}, nil
				case 4:
					if len(request.Inputs) != 1 || request.Inputs[0].Kind != agent.InputToolResult || request.Inputs[0].Tool != "subagent_wait" {
						t.Fatalf("wait continuation inputs = %+v", request.Inputs)
					}
					output := request.Inputs[0].Text
					if !strings.Contains(output, "Subagent subagent-1 (model: balanced, thinking: low):\nfinding for review alpha") || !strings.Contains(output, "Subagent subagent-2 (model: balanced, thinking: low):\nfinding for review beta") {
						t.Fatalf("wait output = %q", output)
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

	arguments, err := parseAgentArguments([]string{
		"--model", "model",
		"--fast-model", "fast-model",
		"--balanced-model", "balanced-model",
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolveTestAgentConfig(arguments, runtime)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newAgentSession(config, runtime, driver.instance)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := session.engine.Run(context.Background(), "review in parallel", func(agent.Event) error { return nil })
	if err := session.finish(runErr); err != nil {
		t.Fatal(err)
	}
	if driver.instance.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", driver.instance.closeCalls)
	}
	if result.Text != "combined answer" || mainCalls != 4 {
		t.Fatalf("result = %+v, main calls = %d", result, mainCalls)
	}

	mu.Lock()
	defer mu.Unlock()
	if factoryCalls != 3 || len(childRequests) != 2 {
		t.Fatalf("factory calls = %d, child requests = %d", factoryCalls, len(childRequests))
	}
	var tasks []string
	for _, request := range childRequests {
		if request.Model != "balanced-model" || request.ThinkingLevel != agent.ThinkingLow || !strings.Contains(request.Instructions, projectInstructions) || !strings.Contains(request.Instructions, "Current working directory: "+filepath.ToSlash(cwd)) {
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

func TestBackendAuthenticationCapabilitiesAreOptional(t *testing.T) {
	driver := &providerOnlyDriver{instance: &providerOnlyInstance{}}
	instance, err := configureBackendInstance(driver, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if driver.instance.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", driver.instance.closeCalls)
	}

	registry, err := backend.NewRegistry("provider-only", driver)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(t.TempDir(), &stdout, &stderr, nil)
	runtime.backends = registry
	for _, command := range []string{"login", "logout"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command}, runtime); code != exitFailure || !strings.Contains(stderr.String(), "does not support "+command) {
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
		wantText   string
	}{
		{name: "browser", arguments: []string{"login"}, wantText: "Open this URL"},
		{name: "device", arguments: []string{"login", "--device-auth"}, wantDevice: true, wantText: "ABCD-EFGH"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runtime := testRuntime(cwd, &stdout, &stderr, nil)
			driver := testBackendDriver(t, runtime)

			if code := run(test.arguments, runtime); code != exitSuccess || driver.loginDevice != test.wantDevice || !driver.interactionCall || !strings.Contains(stderr.String(), test.wantText) || stdout.String() != "Logged in with Test Provider.\n" {
				t.Fatalf("code=%d device=%v stdout=%q stderr=%q", code, driver.loginDevice, stdout.String(), stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	driver := testBackendDriver(t, runtime)
	if code := run([]string{"logout"}, runtime); code != exitSuccess || driver.logoutCalls != 1 || stdout.String() != "Logged out.\n" {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, driver.logoutCalls, stdout.String(), stderr.String())
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
				driver.instance.checkCredentialsErr = errors.New("not logged in; run 'eul login'")
			}

			code := run(test.arguments, runtime)
			combined := stdout.String() + stderr.String()
			if code != test.wantCode || !strings.Contains(combined, test.want) {
				t.Fatalf("run() code=%d stdout=%q stderr=%q, want code=%d containing %q", code, stdout.String(), stderr.String(), test.wantCode, test.want)
			}
			if test.missingAuth && driver.instance.closeCalls != 1 {
				t.Fatalf("backend close calls = %d, want 1", driver.instance.closeCalls)
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

func writeMainTestLSPConfig(t *testing.T, cwd string) {
	t.Helper()
	content := `[{"name":"gopls","command":"gopls","languageID":"go","extensions":[".go"]}]`
	if err := os.WriteFile(filepath.Join(cwd, "lsp.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer, environment map[string]string) appRuntime {
	values := maps.Clone(environment)
	backendInstance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
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
		instance: backendInstance,
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
		newToolset: func(cwd string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
			tools := []tool.Tool{tool.NewRead(cwd)}
			if access == fullToolAccess {
				tools = append(tools, tool.NewWrite(cwd), tool.NewEdit(cwd), tool.NewBash(cwd))
			}
			tools = append(tools, additional...)
			return tool.NewRegistry(tools)
		},
		openURL: func(string) error { return nil },
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

func resolveTestAgentConfig(arguments agentArguments, runtime appRuntime) (agentConfig, error) {
	driver, err := runtime.backends.Lookup(arguments.provider)
	if err != nil {
		return agentConfig{}, err
	}
	return resolveAgentConfig(arguments, runtime, driver.Descriptor(), driver.ModelDefaults())
}
