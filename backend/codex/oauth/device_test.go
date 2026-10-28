package oauth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
)

func TestDeviceLoginAndLogout(t *testing.T) {
	access := testJWT(t, "device-account", "device")
	polls := 0
	path := filepath.Join(t.TempDir(), "eul", "auth.json")
	manager := newTestManager(path, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "ABCD-EFGH", "interval": "0"})
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				writeTestJSON(t, writer, map[string]any{"error": map[string]string{"code": "deviceauth_authorization_pending"}})
				return
			}
			writeTestJSON(t, writer, map[string]any{"authorization_code": "device-code", "code_verifier": "device-verifier", "code_challenge": "ignored"})
		case "/oauth/token":
			if err := request.ParseForm(); err != nil || request.Form.Get("redirect_uri") != authBaseURL+deviceRedirectPath {
				t.Errorf("device exchange form=%v error=%v", request.Form, err)
			}
			writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "device-refresh", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	})

	shown := backend.DeviceCode{}
	if err := manager.LoginDevice(context.Background(), func(code backend.DeviceCode) error { shown = code; return nil }); err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if shown.UserCode != "ABCD-EFGH" || shown.VerificationURL != authBaseURL+"/codex/device" || credential.AccountID != "device-account" || polls != 2 {
		t.Fatalf("shown=%+v credential=%+v polls=%d", shown, credential, polls)
	}

	if err := manager.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential still exists: %v", err)
	}
}

func TestDeviceExchangeUsesPollingDeadline(t *testing.T) {
	access := testJWT(t, "device-account", "deadline")
	sawDeadline := false
	manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "code", "interval": 0})
		case "/api/accounts/deviceauth/token":
			writeTestJSON(t, writer, map[string]any{"authorization_code": "authorization", "code_verifier": "verifier"})
		case "/oauth/token":
			deadline, ok := request.Context().Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > authorizationTimeout {
				t.Errorf("token exchange deadline ok=%t remaining=%s", ok, remaining)
			}
			sawDeadline = ok
			writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	})
	if err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Fatal("token exchange did not use polling context")
	}
}

func TestDeviceLoginStopsOnExplicitDenial(t *testing.T) {
	polls := 0
	manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "ABCD-EFGH", "interval": "0"})
		case "/api/accounts/deviceauth/token":
			polls++
			writer.WriteHeader(http.StatusForbidden)
			writeTestJSON(t, writer, map[string]any{"error": map[string]string{"code": "access_denied"}})
		default:
			http.NotFound(writer, request)
		}
	})
	err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || polls != 1 {
		t.Fatalf("Login() error=%v polls=%d", err, polls)
	}
}
