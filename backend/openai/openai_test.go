package openai

import (
	"context"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/openai/codex"
	"github.com/eul-ai/eul/backend/openai/oauth"
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

func TestDriverProvidesModelDefaults(t *testing.T) {
	defaults := New().ModelDefaults()
	want := backend.ModelDefaults{
		Main:     "gpt-5.6-sol",
		Fast:     "gpt-5.6-luna",
		Balanced: "gpt-5.6-terra",
	}
	if defaults != want {
		t.Fatalf("defaults = %+v, want %+v", defaults, want)
	}
}

func TestDriverConfiguresAuthenticationAndProviderCreation(t *testing.T) {
	manager := &fakeManager{credential: oauth.AccessCredential{AccessToken: "access", AccountID: "account"}}
	driver := New()
	var managerHome string
	driver.newManager = func(home string) (oauthManager, error) {
		managerHome = home
		return manager, nil
	}
	var gotOptions codex.Options
	driver.newProvider = func(source codex.CodexTokenSource, options codex.Options) (agent.Provider, error) {
		gotOptions = options
		credential, err := source.Token(context.Background())
		if err != nil {
			return nil, err
		}
		if credential.AccessToken != "access" || credential.AccountID != "account" {
			t.Fatalf("credential = %+v", credential)
		}
		return fakeProvider{}, nil
	}

	configured, err := driver.Configure(backend.Options{Home: "/config/eul"})
	if err != nil {
		t.Fatal(err)
	}
	checker, ok := configured.(backend.CredentialChecker)
	if !ok {
		t.Fatal("configured backend does not check credentials")
	}
	if err := checker.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider, err := configured.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(fakeProvider); !ok {
		t.Fatalf("provider = %T", provider)
	}
	if err := configured.Close(); err != nil {
		t.Fatal(err)
	}
	if managerHome != "/config/eul" || manager.resolveCalls != 2 || gotOptions != (codex.Options{}) {
		t.Fatalf("home=%q resolveCalls=%d options=%+v", managerHome, manager.resolveCalls, gotOptions)
	}
}

func TestDriverBridgesLoginAndLogout(t *testing.T) {
	manager := &fakeManager{}
	driver := New()
	driver.newManager = func(string) (oauthManager, error) { return manager, nil }

	browserURL := ""
	if err := driver.Login(context.Background(), backend.AuthOptions{}, backend.Interaction{
		OpenURL: func(url string) error {
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
	if err := driver.Login(context.Background(), backend.AuthOptions{Device: true}, backend.Interaction{
		DeviceCode: func(url, code string) error {
			verificationURL, userCode = url, code
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != oauth.LoginDevice || verificationURL == "" || userCode != "CODE" {
		t.Fatalf("device method=%q URL=%q code=%q", manager.loginMethod, verificationURL, userCode)
	}

	if err := driver.Logout(context.Background(), backend.AuthOptions{}); err != nil {
		t.Fatal(err)
	}
	if manager.loginCalls != 2 || manager.logoutCalls != 1 {
		t.Fatalf("login calls=%d logout calls=%d", manager.loginCalls, manager.logoutCalls)
	}
}
