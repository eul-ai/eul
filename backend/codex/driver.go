package codex

import (
	"context"
	"path/filepath"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/client"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

const (
	ID            = "openai-codex"
	Name          = "OpenAI Codex"
	ModelFast     = client.ModelGPT56Luna
	ModelBalanced = client.ModelGPT56Terra
	ModelMain     = client.ModelGPT56Sol
)

type oauthManager interface {
	Login(context.Context, backend.LoginMethod, backend.LoginInteraction) error
	Resolve(context.Context) (oauth.AccessCredential, error)
	Logout(context.Context) error
}

type Driver struct {
	newManager func(string) (oauthManager, error)
	newClient  func(client.TokenSource) (*client.Client, error)
}

var (
	_ backend.Driver                = (*Driver)(nil)
	_ backend.Runtime               = (*runtime)(nil)
	_ backend.CredentialChecker     = (*runtime)(nil)
	_ backend.Authenticator         = (*runtime)(nil)
	_ backend.UsageProvider         = (*runtime)(nil)
	_ backend.ModelMetadataProvider = (*runtime)(nil)
)

func New() *Driver {
	return &Driver{
		newManager: func(path string) (oauthManager, error) {
			return oauth.NewManager(path, oauth.Options{}), nil
		},
		newClient: func(source client.TokenSource) (*client.Client, error) {
			return client.New(source, client.Options{})
		},
	}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{
		ID:   ID,
		Name: Name,
		DefaultModels: backend.ModelDefaults{
			Main:     ModelMain,
			Fast:     ModelFast,
			Balanced: ModelBalanced,
		},
	}
}

func (driver *Driver) Open(options backend.Options) (backend.Runtime, error) {
	manager, err := driver.newManager(filepath.Join(options.Home, "auth.json"))
	if err != nil {
		return nil, err
	}
	return &runtime{
		manager:   manager,
		newClient: driver.newClient,
	}, nil
}

type runtime struct {
	manager   oauthManager
	newClient func(client.TokenSource) (*client.Client, error)
}

func (configured *runtime) Login(ctx context.Context, method backend.LoginMethod, interaction backend.LoginInteraction) error {
	return configured.manager.Login(ctx, method, interaction)
}

func (configured *runtime) Logout(ctx context.Context) error {
	return configured.manager.Logout(ctx)
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	_, err := configured.manager.Resolve(ctx)
	return err
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	return configured.newClient(oauthTokenSource{manager: configured.manager})
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	return backend.ModelMetadata{
		ContextWindow:  client.ContextWindow(model),
		ThinkingLevels: client.SupportedThinkingLevels(model),
		FastMode:       client.FastModeAvailable(model),
	}
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	client, err := configured.newClient(oauthTokenSource{manager: configured.manager})
	if err != nil {
		return backend.AccountUsage{}, err
	}
	usage, err := client.Usage(ctx)
	windows := make([]backend.UsageWindow, len(usage.Windows))
	for index, window := range usage.Windows {
		windows[index] = backend.UsageWindow{
			Duration:    window.Duration,
			UsedPercent: window.UsedPercent,
			ResetsAt:    window.ResetsAt,
		}
	}
	return backend.AccountUsage{Windows: windows}, err
}

func (*runtime) Close() error {
	return nil
}

type oauthTokenSource struct {
	manager oauthManager
}

func (source oauthTokenSource) Token(ctx context.Context) (client.Credential, error) {
	credential, err := source.manager.Resolve(ctx)
	if err != nil {
		return client.Credential{}, err
	}
	return client.Credential{AccessToken: credential.AccessToken, AccountID: credential.AccountID}, nil
}
