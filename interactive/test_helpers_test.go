package interactive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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
	descriptor backend.Descriptor
	runtime    *fakeBackendRuntime
}

func (driver *fakeBackendDriver) Descriptor() backend.Descriptor { return driver.descriptor }
func (driver *fakeBackendDriver) Open(backend.Options) (backend.Runtime, error) {
	return driver.runtime, nil
}

func testRuntime(cwd string, stdout, stderr *bytes.Buffer) runtime {
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	driver := &fakeBackendDriver{
		descriptor: backend.Descriptor{ID: "test", Name: "Test Provider"},
		runtime:    backendRuntime,
	}
	backends, err := backend.NewRegistry("test", driver)
	if err != nil {
		panic(err)
	}

	return runtime{
		stdin:       strings.NewReader("/exit\n"),
		stdout:      stdout,
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: func() (string, error) { return filepath.Join(cwd, ".test-home"), nil },
		backends:    backends,
		providerConfigs: map[string]ProviderConfig{
			"test": {MainModel: "gpt-5.6-sol", FastModel: "gpt-5.6-luna", BalancedModel: "gpt-5.6-terra"},
		},
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

func testBackendDriver(t *testing.T, runtime runtime) *fakeBackendDriver {
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

func resolveTestConfig(options Options, runtime runtime) (resolvedConfig, error) {
	options.NoSandbox = true
	driver, err := runtime.backends.Lookup(options.Provider)
	if err != nil {
		return resolvedConfig{}, err
	}
	return resolveConfig(options, runtime, driver.Descriptor(), runtime.providerConfigs[driver.Descriptor().ID])
}

func stringPointer(value string) *string {
	return &value
}

func writeTestLSPConfig(t *testing.T, cwd string) {
	t.Helper()
	content := `[{"name":"gopls","command":"gopls","languageID":"go","extensions":[".go"]}]`
	if err := os.WriteFile(filepath.Join(cwd, "lsp.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildToolset(cwd string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	return buildToolsetWithHomeAndNetworkAuthorizer(cwd, "", access, false, nil, additional...)
}

func buildToolsetWithHome(cwd, home string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	return buildToolsetWithHomeAndNetworkAuthorizer(cwd, home, access, false, nil, additional...)
}
