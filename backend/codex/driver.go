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
	LoginBrowser(context.Context, func(string) error) error
	LoginDevice(context.Context, func(backend.DeviceCode) error) error
	Resolve(context.Context) (oauth.AccessCredential, error)
	Logout(context.Context) error
}

type Driver struct {
	newManager     func(string) (oauthManager, error)
	newClient      func(client.TokenSource) (*client.Client, error)
	newUsageClient func(client.TokenSource) (*client.Client, error)
}

var (
	_ backend.Driver                = (*Driver)(nil)
	_ backend.Runtime               = (*runtime)(nil)
	_ backend.CredentialChecker     = (*runtime)(nil)
	_ backend.BrowserAuthenticator  = (*runtime)(nil)
	_ backend.DeviceAuthenticator   = (*runtime)(nil)
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
		newUsageClient: func(source client.TokenSource) (*client.Client, error) {
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
	source := oauthTokenSource{manager: manager}
	usage, err := driver.newUsageClient(source)
	if err != nil {
		return nil, err
	}
	return &runtime{
		manager:     manager,
		tokenSource: source,
		newClient:   driver.newClient,
		usageClient: usage,
	}, nil
}

type runtime struct {
	manager     oauthManager
	tokenSource client.TokenSource
	newClient   func(client.TokenSource) (*client.Client, error)
	usageClient *client.Client
}

func (configured *runtime) LoginBrowser(ctx context.Context, presentURL func(string) error) error {
	return configured.manager.LoginBrowser(ctx, presentURL)
}

func (configured *runtime) LoginDevice(ctx context.Context, presentCode func(backend.DeviceCode) error) error {
	return configured.manager.LoginDevice(ctx, presentCode)
}

func (configured *runtime) Logout(ctx context.Context) error {
	return configured.manager.Logout(ctx)
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	_, err := configured.manager.Resolve(ctx)
	return err
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	return configured.newClient(configured.tokenSource)
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	metadata := client.MetadataFor(model)
	return backend.ModelMetadata{
		ContextWindow:  metadata.ContextWindow,
		ThinkingLevels: metadata.ThinkingLevels,
		FastMode:       metadata.FastMode,
	}
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	return configured.usageClient.Usage(ctx)
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
