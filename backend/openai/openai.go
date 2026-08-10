package openai

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/openai/codex"
	"github.com/eul-ai/eul/backend/openai/oauth"
)

const (
	ID   = "openai-codex"
	Name = "OpenAI Codex"
)

type oauthManager interface {
	Login(context.Context, oauth.LoginMethod, oauth.Interaction) error
	Resolve(context.Context) (oauth.AccessCredential, error)
	Logout(context.Context) error
}

type Driver struct {
	newManager  func(string) (oauthManager, error)
	newProvider func(codex.TokenSource) (agent.Provider, error)
}

var (
	_ backend.Driver            = (*Driver)(nil)
	_ backend.Authenticator     = (*Driver)(nil)
	_ backend.Runtime           = (*runtime)(nil)
	_ backend.CredentialChecker = (*runtime)(nil)
)

func New() *Driver {
	return &Driver{
		newManager: func(home string) (oauthManager, error) {
			path, err := oauth.DefaultCredentialPath(home)
			if err != nil {
				return nil, err
			}
			return oauth.NewManager(path, oauth.Options{}), nil
		},
		newProvider: func(source codex.TokenSource) (agent.Provider, error) {
			return codex.New(source, codex.Options{})
		},
	}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: ID, Name: Name}
}

func (*Driver) ModelDefaults() backend.ModelDefaults {
	return backend.ModelDefaults{
		Main:     "gpt-5.6-sol",
		Fast:     "gpt-5.6-luna",
		Balanced: "gpt-5.6-terra",
	}
}

func (driver *Driver) Open(options backend.Options) (backend.Runtime, error) {
	manager, err := driver.newManager(options.Home)
	if err != nil {
		return nil, err
	}
	return &runtime{
		manager:     manager,
		newProvider: driver.newProvider,
	}, nil
}

func (driver *Driver) Login(ctx context.Context, options backend.AuthOptions, interaction backend.Interaction) error {
	manager, err := driver.newManager(options.Home)
	if err != nil {
		return err
	}
	method := oauth.LoginBrowser
	if options.Device {
		method = oauth.LoginDevice
	}
	oauthInteraction := oauth.Interaction{AuthURL: interaction.OpenURL}
	if interaction.DeviceCode != nil {
		oauthInteraction.DeviceCode = func(code oauth.DeviceCode) error {
			return interaction.DeviceCode(code.VerificationURL, code.UserCode)
		}
	}
	return manager.Login(ctx, method, oauthInteraction)
}

func (driver *Driver) Logout(ctx context.Context, options backend.AuthOptions) error {
	manager, err := driver.newManager(options.Home)
	if err != nil {
		return err
	}
	return manager.Logout(ctx)
}

type runtime struct {
	manager     oauthManager
	newProvider func(codex.TokenSource) (agent.Provider, error)
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	_, err := configured.manager.Resolve(ctx)
	return err
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	return configured.newProvider(oauthTokenSource{manager: configured.manager})
}

func (*runtime) Close() error {
	return nil
}

type oauthTokenSource struct {
	manager oauthManager
}

func (source oauthTokenSource) Token(ctx context.Context) (codex.Credential, error) {
	credential, err := source.manager.Resolve(ctx)
	if err != nil {
		return codex.Credential{}, err
	}
	return codex.Credential{AccessToken: credential.AccessToken, AccountID: credential.AccountID}, nil
}
