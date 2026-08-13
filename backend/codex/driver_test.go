package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/client"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

type fakeManager struct {
	credential   oauth.AccessCredential
	resolveErr   error
	loginErr     error
	logoutErr    error
	loginMethod  backend.LoginMethod
	loginCalls   int
	logoutCalls  int
	resolveCalls int
}

func (manager *fakeManager) Login(_ context.Context, method backend.LoginMethod, interaction backend.LoginInteraction) error {
	manager.loginCalls++
	manager.loginMethod = method
	switch method {
	case backend.LoginBrowser:
		if interaction.AuthURL != nil {
			_ = interaction.AuthURL("https://example.test/login")
		}
	case backend.LoginDevice:
		if interaction.DeviceCode != nil {
			_ = interaction.DeviceCode(backend.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "CODE"})
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

func TestDriverOpensRuntimeWithAuthenticationAndProviderCreation(t *testing.T) {
	manager := &fakeManager{credential: oauth.AccessCredential{AccessToken: "access", AccountID: "account"}}
	driver := New()
	var managerHome string
	driver.newManager = func(home string) (oauthManager, error) {
		managerHome = home
		return manager, nil
	}
	driver.newClient = func(source client.TokenSource) (*client.Client, error) {
		credential, err := source.Token(context.Background())
		if err != nil {
			return nil, err
		}
		if credential.AccessToken != "access" || credential.AccountID != "account" {
			t.Fatalf("credential = %+v", credential)
		}
		return client.New(source, client.Options{})
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
	if _, ok := provider.(*client.Client); !ok {
		t.Fatalf("provider = %T", provider)
	}
	if err := backendRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	if managerHome != "/config/eul" || manager.resolveCalls != 2 {
		t.Fatalf("home=%q resolveCalls=%d", managerHome, manager.resolveCalls)
	}
}

func TestDriverModelDefaultsAreSupported(t *testing.T) {
	defaults := New().Descriptor().DefaultModels
	configured := &runtime{}
	for _, model := range []string{defaults.Main, defaults.Fast, defaults.Balanced} {
		if metadata := configured.ModelMetadata(model); !metadata.FastMode || metadata.ContextWindow == 0 {
			t.Fatalf("model %q metadata = %+v", model, metadata)
		}
	}
}

func TestRuntimeLoadsAccountUsage(t *testing.T) {
	manager := &fakeManager{credential: oauth.AccessCredential{AccessToken: "access", AccountID: "account"}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/codex/usage" {
			t.Errorf("usage path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":3600}}}`))
	}))
	defer server.Close()

	configured := &runtime{
		manager: manager,
		newClient: func(source client.TokenSource) (*client.Client, error) {
			return client.New(source, client.Options{BaseURL: server.URL})
		},
	}
	usage, err := configured.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Windows) != 1 || usage.Windows[0].UsedPercent != 25 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestRuntimeBridgesLoginAndLogout(t *testing.T) {
	manager := &fakeManager{}
	configured := &runtime{manager: manager}

	browserURL := ""
	if err := configured.Login(context.Background(), backend.LoginBrowser, backend.LoginInteraction{
		AuthURL: func(url string) error {
			browserURL = url
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != backend.LoginBrowser || browserURL == "" {
		t.Fatalf("browser method=%q URL=%q", manager.loginMethod, browserURL)
	}

	verificationURL, userCode := "", ""
	if err := configured.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{
		DeviceCode: func(code backend.DeviceCode) error {
			verificationURL, userCode = code.VerificationURL, code.UserCode
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != backend.LoginDevice || verificationURL == "" || userCode != "CODE" {
		t.Fatalf("device method=%q URL=%q code=%q", manager.loginMethod, verificationURL, userCode)
	}

	if err := configured.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.loginCalls != 2 || manager.logoutCalls != 1 {
		t.Fatalf("login calls=%d logout calls=%d", manager.loginCalls, manager.logoutCalls)
	}
}
