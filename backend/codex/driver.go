package codex

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	api "github.com/eul-ai/eul/backend/codex/api"
	"github.com/eul-ai/eul/backend/codex/oauth"
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
	newProvider func(api.TokenSource) (agent.Provider, error)
}

var (
	_ backend.Driver            = (*Driver)(nil)
	_ backend.Runtime           = (*runtime)(nil)
	_ backend.CredentialChecker = (*runtime)(nil)
	_ oauth.Authenticator       = (*runtime)(nil)
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
		newProvider: func(source api.TokenSource) (agent.Provider, error) {
			return api.New(source, api.Options{})
		},
	}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: ID, Name: Name}
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

type runtime struct {
	manager     oauthManager
	newProvider func(api.TokenSource) (agent.Provider, error)
}

func (configured *runtime) Login(ctx context.Context, method oauth.LoginMethod, interaction oauth.Interaction) error {
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
	return configured.newProvider(oauthTokenSource{manager: configured.manager})
}

func (*runtime) Close() error {
	return nil
}

type oauthTokenSource struct {
	manager oauthManager
}

func (source oauthTokenSource) Token(ctx context.Context) (api.Credential, error) {
	credential, err := source.manager.Resolve(ctx)
	if err != nil {
		return api.Credential{}, err
	}
	return api.Credential{AccessToken: credential.AccessToken, AccountID: credential.AccountID}, nil
}
