package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPreCanceledOperationsDoNotTouchCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	credential := credentials{AccessToken: testJWT(t, "account", "canceled"), RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account"}
	if err := writeCredentials(path, credential); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(path)
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
