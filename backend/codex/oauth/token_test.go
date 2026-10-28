package oauth

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestRefreshPersistsRotatedCredentials(t *testing.T) {
	oldAccess := testJWT(t, "old-account", "old")
	newAccess := testJWT(t, "new-account", "new")
	path := filepath.Join(t.TempDir(), "auth.json")
	manager := newTestManager(path, func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": newAccess, "refresh_token": "new-refresh", "expires_in": 3600})
	})
	old := credentials{AccessToken: oldAccess, RefreshToken: "old-refresh", ExpiresAt: 1, AccountID: "old-account"}
	if err := writeCredentials(path, old); err != nil {
		t.Fatal(err)
	}
	refreshed, err := manager.Resolve(context.Background())
	if err != nil || refreshed.AccountID != "new-account" {
		t.Fatalf("refreshed=%+v error=%v", refreshed, err)
	}
	persisted, readErr := readCredentials(path)
	if readErr != nil || persisted.RefreshToken != "new-refresh" {
		t.Fatalf("persisted=%+v error=%v", persisted, readErr)
	}
}

func TestRefreshRetainsTokenWhenNotRotated(t *testing.T) {
	oldAccess := testJWT(t, "account", "old")
	newAccess := testJWT(t, "account", "new")
	path := filepath.Join(t.TempDir(), "auth.json")
	manager := newTestManager(path, func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": newAccess, "expires_in": 3600})
	})
	old := credentials{AccessToken: oldAccess, RefreshToken: "old-refresh", ExpiresAt: 1, AccountID: "account"}
	if err := writeCredentials(path, old); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := readCredentials(path)
	if err != nil || persisted.AccessToken != newAccess || persisted.RefreshToken != "old-refresh" {
		t.Fatalf("persisted=%+v error=%v", persisted, err)
	}
}
