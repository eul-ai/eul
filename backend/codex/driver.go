package codex

import (
	"context"
	"maps"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

const ID = "openai-codex"

type Driver struct{}

var (
	_ backend.Driver                = (*Driver)(nil)
	_ backend.Runtime               = (*runtime)(nil)
	_ backend.CredentialChecker     = (*runtime)(nil)
	_ backend.BrowserAuthenticator  = (*runtime)(nil)
	_ backend.DeviceAuthenticator   = (*runtime)(nil)
	_ backend.Authenticator         = (*runtime)(nil)
	_ backend.UsageProvider         = (*runtime)(nil)
	_ backend.ModelMetadataProvider = (*runtime)(nil)
	_ backend.ModelValidator        = (*runtime)(nil)
)

func New() *Driver {
	return &Driver{}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{
		ID:   ID,
		Name: "OpenAI Codex",
		DefaultModels: backend.ModelDefaults{
			Main:     modelMain,
			Fast:     modelFast,
			Balanced: modelBalanced,
		},
	}
}

func (*Driver) Open(options backend.Options) (backend.Runtime, error) {
	return &runtime{manager: oauth.NewManager(filepath.Join(options.Home, "auth.json"))}, nil
}

type runtime struct {
	manager    *oauth.Manager
	httpClient *http.Client
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
	return newClient(configured.manager, configured.httpClient), nil
}

func (*runtime) ModelMetadata(model string) backend.ModelMetadata {
	return modelMetadata(model)
}

func (*runtime) ValidateModel(model string) error {
	if _, ok := models[model]; ok {
		return nil
	}
	return backend.NewModelNotSupportedError("openai codex", model, slices.Collect(maps.Keys(models)))
}

func (*runtime) Close() error {
	return nil
}
