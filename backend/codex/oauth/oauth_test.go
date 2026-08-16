package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewManagerCopiesInjectedHTTPClientBeforeApplyingPolicy(t *testing.T) {
	redirect := func(*http.Request, []*http.Request) error { return nil }
	transport := http.DefaultTransport
	injected := &http.Client{Transport: transport, CheckRedirect: redirect}

	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: injected})
	if manager.httpClient == injected || manager.httpClient.Transport != transport || manager.httpClient.Timeout != defaultHTTPTimeout {
		t.Fatalf("owned client=%p injected=%p transport=%T timeout=%s", manager.httpClient, injected, manager.httpClient.Transport, manager.httpClient.Timeout)
	}
	if injected.Timeout != 0 || injected.CheckRedirect == nil {
		t.Fatalf("injected client was mutated: timeout=%s redirect missing=%t", injected.Timeout, injected.CheckRedirect == nil)
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
	if err := manager.httpClient.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("owned redirect policy error = %v", err)
	}
	if err := injected.CheckRedirect(request, nil); err != nil {
		t.Fatalf("injected redirect policy changed: %v", err)
	}
}

func TestPreCanceledOperationsDoNotTouchCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	credential := credentials{Version: 1, Type: "oauth", AccessToken: testJWT(t, "account", "canceled"), RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account"}
	if err := writeCredentials(path, credential); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(path, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Resolve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := manager.Logout(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Logout() error = %v", err)
	}
	interactionCalled := false
	if err := manager.LoginBrowser(ctx, func(string) error { interactionCalled = true; return nil }); !errors.Is(err, context.Canceled) || interactionCalled {
		t.Fatalf("Login() error=%v interactionCalled=%v", err, interactionCalled)
	}
	if persisted, err := readCredentials(path); err != nil || persisted.AccessToken != credential.AccessToken {
		t.Fatalf("persisted=%+v error=%v", persisted, err)
	}
}
