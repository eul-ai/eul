package codex

import (
	"context"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	api "github.com/eul-ai/eul/backend/codex/api"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

type fakeManager struct {
	credential   oauth.AccessCredential
	resolveErr   error
	loginErr     error
	logoutErr    error
	loginMethod  oauth.LoginMethod
	loginCalls   int
	logoutCalls  int
	resolveCalls int
}

func (manager *fakeManager) Login(_ context.Context, method oauth.LoginMethod, interaction oauth.Interaction) error {
	manager.loginCalls++
	manager.loginMethod = method
	switch method {
	case oauth.LoginBrowser:
		if interaction.AuthURL != nil {
			_ = interaction.AuthURL("https://example.test/login")
		}
	case oauth.LoginDevice:
		if interaction.DeviceCode != nil {
			_ = interaction.DeviceCode(oauth.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "CODE"})
		}
	}
	return manager.loginErr
}

func (manager *fakeManager) Resolve(context.Context) (oauth.AccessCredential, error) {
	manager.resolveCalls++
	return manager.credential, manager.resolveErr
}

func (manager *fakeManager) Logout(context.Context) error {
	manager.logoutCalls++
	return manager.logoutErr
}

type fakeProvider struct{}

func (fakeProvider) Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error) {
	return agent.Response{}, nil
}

func TestDriverOpensRuntimeWithAuthenticationAndProviderCreation(t *testing.T) {
	manager := &fakeManager{credential: oauth.AccessCredential{AccessToken: "access", AccountID: "account"}}
	driver := New()
	var managerHome string
	driver.newManager = func(home string) (oauthManager, error) {
		managerHome = home
		return manager, nil
	}
	driver.newProvider = func(source api.TokenSource) (agent.Provider, error) {
		credential, err := source.Token(context.Background())
		if err != nil {
			return nil, err
		}
		if credential.AccessToken != "access" || credential.AccountID != "account" {
			t.Fatalf("credential = %+v", credential)
		}
		return fakeProvider{}, nil
	}

	backendRuntime, err := driver.Open(backend.Options{Home: "/config/eul"})
	if err != nil {
		t.Fatal(err)
	}
	checker, ok := backendRuntime.(backend.CredentialChecker)
	if !ok {
		t.Fatal("configured backend does not check credentials")
	}
	if err := checker.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider, err := backendRuntime.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(fakeProvider); !ok {
		t.Fatalf("provider = %T", provider)
	}
	if err := backendRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	if managerHome != "/config/eul" || manager.resolveCalls != 2 {
		t.Fatalf("home=%q resolveCalls=%d", managerHome, manager.resolveCalls)
	}
}

func TestDriverModelCatalogConstants(t *testing.T) {
	for _, model := range []string{api.ModelGPT56Sol, api.ModelGPT56Luna, api.ModelGPT56Terra} {
		if metadata := (&api.Client{}).ModelMetadata(model); !metadata.FastMode || metadata.ContextWindow == 0 {
			t.Fatalf("model %q metadata = %+v", model, metadata)
		}
	}
}

func TestRuntimeBridgesLoginAndLogout(t *testing.T) {
	manager := &fakeManager{}
	configured := &runtime{manager: manager}

	browserURL := ""
	if err := configured.Login(context.Background(), oauth.LoginBrowser, oauth.Interaction{
		AuthURL: func(url string) error {
			browserURL = url
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != oauth.LoginBrowser || browserURL == "" {
		t.Fatalf("browser method=%q URL=%q", manager.loginMethod, browserURL)
	}

	verificationURL, userCode := "", ""
	if err := configured.Login(context.Background(), oauth.LoginDevice, oauth.Interaction{
		DeviceCode: func(code oauth.DeviceCode) error {
			verificationURL, userCode = code.VerificationURL, code.UserCode
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != oauth.LoginDevice || verificationURL == "" || userCode != "CODE" {
		t.Fatalf("device method=%q URL=%q code=%q", manager.loginMethod, verificationURL, userCode)
	}

	if err := configured.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.loginCalls != 2 || manager.logoutCalls != 1 {
		t.Fatalf("login calls=%d logout calls=%d", manager.loginCalls, manager.logoutCalls)
	}
}
