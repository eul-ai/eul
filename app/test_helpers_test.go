package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func (providerFunction) ModelMetadata(string) backend.ModelMetadata {
	return backend.ModelMetadata{ThinkingLevels: agent.ThinkingLevels(), FastMode: true}
}

type fakeBackendRuntime struct {
	checkCredentialsErr   error
	checkCredentialsCalls int
	initializeModelsErr   error
	initializeModelsCalls int
	initializationOrder   *[]string
	closeErr              error
	closeCalls            int
	newProvider           func() (agent.Provider, error)
	metadata              func(string) backend.ModelMetadata
	usage                 func(context.Context) (backend.AccountUsage, error)
}

type metadataFreeBackendRuntime struct {
	backend.Runtime
}

func (runtime *fakeBackendRuntime) CheckCredentials(context.Context) error {
	runtime.checkCredentialsCalls++
	if runtime.initializationOrder != nil {
		*runtime.initializationOrder = append(*runtime.initializationOrder, "credentials")
	}
	return runtime.checkCredentialsErr
}

func (runtime *fakeBackendRuntime) InitializeModels(context.Context) error {
	runtime.initializeModelsCalls++
	if runtime.initializationOrder != nil {
		*runtime.initializationOrder = append(*runtime.initializationOrder, "models")
	}
	return runtime.initializeModelsErr
}

func (runtime *fakeBackendRuntime) NewProvider() (agent.Provider, error) {
	return runtime.newProvider()
}

func (runtime *fakeBackendRuntime) ModelMetadata(model string) backend.ModelMetadata {
	if runtime.metadata != nil {
		return runtime.metadata(model)
	}
	return backend.ModelMetadata{ThinkingLevels: agent.ThinkingLevels(), FastMode: true}
}

func (runtime *fakeBackendRuntime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if runtime.usage != nil {
		return runtime.usage(ctx)
	}
	return backend.AccountUsage{}, nil
}

func (runtime *fakeBackendRuntime) Close() error {
	runtime.closeCalls++
	return runtime.closeErr
}

type fakeBackendDriver struct {
	descriptor backend.Descriptor
	runtime    *fakeBackendRuntime
}

func (driver *fakeBackendDriver) Descriptor() backend.Descriptor { return driver.descriptor }
func (driver *fakeBackendDriver) Open(backend.Options) (backend.Runtime, error) {
	return driver.runtime, nil
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer) environment {
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

	return environment{
		stdin:       strings.NewReader("/exit\n"),
		stdout:      stdout,
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: func() (string, error) { return filepath.Join(cwd, ".test-home"), nil },
		backends:    backends,
		newToolset: func(cwd string, access toolAccess, noSandbox bool, authorizeNetwork tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
			tools := []tool.Tool{tool.NewRead(cwd)}
			if access == fullToolAccess {
				bash := tool.NewBashWithNetworkAuthorizer(cwd, authorizeNetwork)
				if noSandbox {
					bash = tool.NewBashWithoutSandbox(cwd)
				}
				tools = append(tools, tool.NewWrite(cwd), tool.NewEdit(cwd), bash)
			}
			tools = append(tools, additional...)
			return tool.NewRegistry(tools)
		},
	}
}

func testBackendDriver(t *testing.T, env environment) *fakeBackendDriver {
	t.Helper()
	driver, err := env.backends.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	fake, ok := driver.(*fakeBackendDriver)
	if !ok {
		t.Fatalf("backend driver = %T", driver)
	}
	return fake
}

func resolveTestConfig(options Options, env environment) (resolvedConfig, error) {
	options.NoSandbox = true
	driver, err := env.backends.Lookup(options.Provider)
	if err != nil {
		return resolvedConfig{}, err
	}
	return resolveConfig(options, env, driver.Descriptor())
}

func stringPointer(value string) *string {
	return &value
}

func sessionStoreTestAgentCheckpoint(t testing.TB) agent.Checkpoint {
	t.Helper()
	var checkpoint agent.Checkpoint
	if err := json.Unmarshal([]byte(`{"version":3,"context_usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`), &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func sessionStoreTestTerminalCheckpoint(t testing.TB, prompt string) terminal.Checkpoint {
	t.Helper()
	return sessionStoreTestTerminalBlocks(t, []map[string]any{{
		"kind": 0,
		"text": prompt,
	}})
}

func sessionStoreTestTerminalBlocks(t testing.TB, blocks []map[string]any) terminal.Checkpoint {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"version": 2,
		"blocks":  blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint terminal.Checkpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}
