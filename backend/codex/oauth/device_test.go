package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/testhttp"
)

func TestDeviceLoginAndLogout(t *testing.T) {
	access := testJWT(t, "device-account", "device")
	polls := 0
	var server *testhttp.Server
	server = newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "ABCD-EFGH", "interval": "1"})
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				writeTestJSON(t, writer, map[string]any{"error": map[string]string{"code": "deviceauth_authorization_pending"}})
				return
			}
			writeTestJSON(t, writer, map[string]any{"authorization_code": "device-code", "code_verifier": "device-verifier", "code_challenge": "ignored"})
		case "/oauth/token":
			if err := request.ParseForm(); err != nil || request.Form.Get("redirect_uri") != server.URL+deviceRedirectPath {
				t.Errorf("device exchange form=%v error=%v", request.Form, err)
			}
			writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "device-refresh", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "eul", "auth.json")
	manager := NewManager(path, Options{HTTPClient: server.Client(), AuthBaseURL: server.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
	shown := backend.DeviceCode{}
	if err := manager.LoginDevice(context.Background(), func(code backend.DeviceCode) error { shown = code; return nil }); err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if shown.UserCode != "ABCD-EFGH" || shown.VerificationURL != server.URL+"/codex/device" || credential.AccountID != "device-account" || polls != 2 {
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
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "code", "interval": 1})
		case "/api/accounts/deviceauth/token":
			writeTestJSON(t, writer, map[string]any{"authorization_code": "authorization", "code_verifier": "verifier"})
		case "/oauth/token":
			writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	sawDeadline := false
	baseTransport := server.Client().Transport
	transport := oauthRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth/token" {
			deadline, ok := request.Context().Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > authorizationTimeout {
				t.Errorf("token exchange deadline ok=%t remaining=%s", ok, remaining)
			}
			sawDeadline = ok
		}
		return baseTransport.RoundTrip(request)
	})
	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{
		AuthBaseURL: server.URL,
		HTTPClient:  &http.Client{Transport: transport},
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Fatal("token exchange did not use polling context")
	}
}

func TestParseIntervalValidation(t *testing.T) {
	for _, raw := range []string{`"NaN"`, `"Inf"`, `"-Inf"`, `-1`, `0`, `0.999`, `"0.5"`, `301`} {
		if _, err := parseInterval(json.RawMessage(raw)); err == nil {
			t.Fatalf("parseInterval(%s) succeeded", raw)
		}
	}
	for _, test := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: `1`, want: time.Second},
		{raw: `1.5`, want: 1500 * time.Millisecond},
		{raw: `"1.5"`, want: 1500 * time.Millisecond},
		{raw: `300`, want: 300 * time.Second},
	} {
		if interval, err := parseInterval(json.RawMessage(test.raw)); err != nil || interval != test.want {
			t.Fatalf("parseInterval(%s) = %s, %v, want %s", test.raw, interval, err, test.want)
		}
	}
}

func TestDeviceLoginStopsOnExplicitDenial(t *testing.T) {
	polls := 0
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device-id", "user_code": "ABCD-EFGH", "interval": "1"})
		case "/api/accounts/deviceauth/token":
			polls++
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": "access_denied"}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: server.Client(), AuthBaseURL: server.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
	err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || polls != 1 {
		t.Fatalf("Login() error=%v polls=%d", err, polls)
	}
}
