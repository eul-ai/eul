package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
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
	loginMethod  string
	loginCalls   int
	logoutCalls  int
	resolveCalls int
}

func (manager *fakeManager) LoginBrowser(_ context.Context, presentURL func(string) error) error {
	manager.loginCalls++
	manager.loginMethod = "browser"
	if presentURL != nil {
		_ = presentURL("https://example.test/login")
	}
	return manager.loginErr
}

func (manager *fakeManager) LoginDevice(_ context.Context, presentCode func(backend.DeviceCode) error) error {
	manager.loginCalls++
	manager.loginMethod = "device"
	if presentCode != nil {
		_ = presentCode(backend.DeviceCode{VerificationURL: "https://example.test/device", UserCode: "CODE"})
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
	var managerPath string
	driver.newManager = func(path string) (oauthManager, error) {
		managerPath = path
		return manager, nil
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
	if managerPath != "/config/eul/auth.json" || manager.resolveCalls != 1 {
		t.Fatalf("path=%q resolveCalls=%d", managerPath, manager.resolveCalls)
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
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/codex/usage" {
			t.Errorf("usage path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":3600}}}`)),
		}, nil
	})}

	usage, err := client.NewUsage(oauthTokenSource{manager: manager}, client.UsageOptions{HTTPClient: httpClient, BaseURL: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	configured := &runtime{manager: manager, usageClient: usage}
	got, err := configured.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 1 || got.Windows[0].UsedPercent != 25 {
		t.Fatalf("usage = %+v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRuntimeBridgesLoginAndLogout(t *testing.T) {
	manager := &fakeManager{}
	configured := &runtime{manager: manager}

	browserURL := ""
	if err := configured.LoginBrowser(context.Background(), func(url string) error {
		browserURL = url
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != "browser" || browserURL == "" {
		t.Fatalf("browser method=%q URL=%q", manager.loginMethod, browserURL)
	}

	verificationURL, userCode := "", ""
	if err := configured.LoginDevice(context.Background(), func(code backend.DeviceCode) error {
		verificationURL, userCode = code.VerificationURL, code.UserCode
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if manager.loginMethod != "device" || verificationURL == "" || userCode != "CODE" {
		t.Fatalf("device method=%q URL=%q code=%q", manager.loginMethod, verificationURL, userCode)
	}

	if err := configured.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.loginCalls != 2 || manager.logoutCalls != 1 {
		t.Fatalf("login calls=%d logout calls=%d", manager.loginCalls, manager.logoutCalls)
	}
}
