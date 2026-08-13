package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eul-ai/eul/backend"
)

func TestBrowserLoginPKCEStorageAndRefreshRotation(t *testing.T) {
	const accountID = "account-123"
	accessOne := testJWT(t, accountID, "one")
	accessTwo := testJWT(t, accountID, "two")
	now := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	var authorize url.Values
	tokenCalls := 0
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/oauth/token" {
			t.Errorf("unexpected auth path %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		tokenCalls++
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.Form.Get("grant_type") {
		case "authorization_code":
			mu.Lock()
			captured := authorize
			mu.Unlock()
			hashVerifier := sha256Base64(request.Form.Get("code_verifier"))
			if request.Form.Get("code") != "authorization-code" || request.Form.Get("redirect_uri") != captured.Get("redirect_uri") || hashVerifier != captured.Get("code_challenge") {
				t.Errorf("invalid exchange form: %v", request.Form)
			}
			writeTestJSON(t, writer, map[string]any{"access_token": accessOne, "refresh_token": "refresh-one", "expires_in": 600, "id_token": "ignored"})
		case "refresh_token":
			if request.Form.Get("refresh_token") != "refresh-one" {
				t.Errorf("refresh token = %q", request.Form.Get("refresh_token"))
			}
			writeTestJSON(t, writer, map[string]any{"access_token": accessTwo, "expires_in": 3600})
		default:
			t.Errorf("grant type = %q", request.Form.Get("grant_type"))
		}
	}))
	defer server.Close()

	home := t.TempDir()
	path := filepath.Join(home, "private", "auth.json")
	manager := NewManager(path, Options{AuthBaseURL: server.URL, CallbackAddress: "127.0.0.1:0", Now: func() time.Time { return now }})
	err := manager.Login(context.Background(), backend.LoginBrowser, backend.LoginInteraction{AuthURL: func(raw string) error {
		parsed, err := url.Parse(raw)
		if err != nil {
			return err
		}
		values := parsed.Query()
		if values.Get("client_id") != clientID || values.Get("code_challenge_method") != "S256" || values.Get("state") == "" || values.Get("originator") != "eul" {
			t.Errorf("authorization query = %v", values)
		}
		mu.Lock()
		authorize = values
		mu.Unlock()
		callback := values.Get("redirect_uri") + "?code=authorization-code&state=" + url.QueryEscape(values.Get("state"))
		response, err := http.Get(callback)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != http.StatusOK {
			t.Errorf("callback status = %d", response.StatusCode)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := readCredentials(path)
	if err != nil {
		t.Fatal(err)
	}

	if credential.AccessToken != accessOne || credential.RefreshToken != "refresh-one" || credential.AccountID != accountID {
		t.Fatalf("credential = %+v", credential)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("permissions file=%o directory=%o", fileInfo.Mode().Perm(), directoryInfo.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "refresh-one") || strings.Contains(string(body), "authorization-code") {
		t.Fatalf("stored credential = %s", body)
	}

	now = now.Add(6 * time.Minute)
	refreshed, err := manager.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != accessTwo || tokenCalls != 2 {
		t.Fatalf("refreshed=%+v calls=%d", refreshed, tokenCalls)
	}
	persisted, err := readCredentials(path)
	if err != nil || persisted.AccessToken != accessTwo || persisted.RefreshToken != "refresh-one" {
		t.Fatalf("persisted=%+v error=%v", persisted, err)
	}
}

func TestBrowserCallbackRejectsWrongStateThenAcceptsValidState(t *testing.T) {
	access := testJWT(t, "account", "state")
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh", "expires_in": 3600})
	}))
	defer server.Close()

	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{AuthBaseURL: server.URL, CallbackAddress: "127.0.0.1:0"})
	err := manager.Login(context.Background(), backend.LoginBrowser, backend.LoginInteraction{AuthURL: func(raw string) error {
		parsed, _ := url.Parse(raw)
		redirect := parsed.Query().Get("redirect_uri")
		wrong, err := http.Get(redirect + "?code=secret-code&state=wrong")
		if err != nil {
			return err
		}
		wrong.Body.Close()
		if wrong.StatusCode != http.StatusBadRequest {
			t.Errorf("wrong-state status = %d", wrong.StatusCode)
		}
		valid, err := http.Get(redirect + "?code=valid-code&state=" + url.QueryEscape(parsed.Query().Get("state")))
		if err != nil {
			return err
		}
		valid.Body.Close()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCallbackReportsMissingCode(t *testing.T) {
	result := make(chan callbackResult, 1)
	request := httptest.NewRequest(http.MethodGet, browserCallbackPath+"?state=expected", nil)
	writer := httptest.NewRecorder()

	callbackHandler("expected", result).ServeHTTP(writer, request)
	if writer.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d", writer.Code)
	}
	select {
	case callback := <-result:
		if callback.err == nil || !strings.Contains(callback.err.Error(), "missing") {
			t.Fatalf("callback result = %+v", callback)
		}
	default:
		t.Fatal("missing-code callback did not finish the login")
	}
}

func TestDeviceLoginAndLogout(t *testing.T) {
	access := testJWT(t, "device-account", "device")
	polls := 0
	var server *httptest.Server
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
	manager := NewManager(path, Options{AuthBaseURL: server.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
	shown := backend.DeviceCode{}
	if err := manager.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(code backend.DeviceCode) error { shown = code; return nil }}); err != nil {
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

func TestRefreshPersistsRotatedcredentials(t *testing.T) {
	oldAccess := testJWT(t, "old-account", "old")
	newAccess := testJWT(t, "new-account", "new")
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": newAccess, "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	manager := NewManager(path, Options{AuthBaseURL: server.URL, Now: func() time.Time { return time.Unix(2000, 0) }})
	old := credentials{Version: 1, Type: "oauth", AccessToken: oldAccess, RefreshToken: "old-refresh", ExpiresAt: 1, AccountID: "old-account"}
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
	transport := oauthRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/oauth/token" {
			deadline, ok := request.Context().Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > deviceTimeout {
				t.Errorf("token exchange deadline ok=%t remaining=%s", ok, remaining)
			}
			sawDeadline = ok
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{
		AuthBaseURL: server.URL,
		HTTPClient:  &http.Client{Transport: transport},
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err := manager.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(backend.DeviceCode) error { return nil }}); err != nil {
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

func TestOAuthHTTPRedirectBoundsAndCancellation(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		destinationCalls := 0
		destination := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls++ }))
		defer destination.Close()
		origin := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{AuthBaseURL: origin.URL})
		err := manager.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(backend.DeviceCode) error { return nil }})
		if err == nil || !strings.Contains(err.Error(), "HTTP 307") || destinationCalls != 0 {
			t.Fatalf("error=%v destinationCalls=%d", err, destinationCalls)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", int(maxAuthResponseBytes)+1))
		}))
		defer server.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{AuthBaseURL: server.URL})
		err := manager.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(backend.DeviceCode) error { return nil }})
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device", "user_code": "code", "interval": "1"})
		}))
		defer server.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{AuthBaseURL: server.URL})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := manager.Login(ctx, backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(backend.DeviceCode) error { return nil }})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
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
	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{AuthBaseURL: server.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
	err := manager.Login(context.Background(), backend.LoginDevice, backend.LoginInteraction{DeviceCode: func(backend.DeviceCode) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || polls != 1 {
		t.Fatalf("Login() error=%v polls=%d", err, polls)
	}
}

func TestCredentialFileLockIsExclusiveAndCancelable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	first := NewManager(path, Options{})
	second := NewManager(path, Options{})
	held := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.withFileLock(context.Background(), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := second.withFileLock(ctx, func() error { return errors.New("must not enter") }); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("contending lock error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	entered := false
	if err := second.withFileLock(context.Background(), func() error { entered = true; return nil }); err != nil || !entered {
		t.Fatalf("lock after release entered=%v error=%v", entered, err)
	}
}

func TestPreCanceledOperationsDoNotTouchcredentials(t *testing.T) {
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
	if err := manager.Login(ctx, backend.LoginBrowser, backend.LoginInteraction{AuthURL: func(string) error { interactionCalled = true; return nil }}); !errors.Is(err, context.Canceled) || interactionCalled {
		t.Fatalf("Login() error=%v interactionCalled=%v", err, interactionCalled)
	}
	if persisted, err := readCredentials(path); err != nil || persisted.AccessToken != credential.AccessToken {
		t.Fatalf("persisted=%+v error=%v", persisted, err)
	}
}

func TestDefaultCredentialPathAndInvalidStorage(t *testing.T) {
	home := t.TempDir()
	path, err := DefaultCredentialPath(home)
	if err != nil || path != filepath.Join(home, "auth.json") {
		t.Fatalf("path=%q error=%v", path, err)
	}
	if _, err := DefaultCredentialPath("relative"); err == nil {
		t.Fatal("relative EUL_HOME accepted")
	}

	credentialPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.Symlink("target", credentialPath); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentials(credentialPath, credentials{Version: 1, Type: "oauth"}); err == nil {
		t.Fatal("symlink credential path accepted for writing")
	}
	if _, err := readCredentials(credentialPath); err == nil {
		t.Fatal("symlink credential path accepted for reading")
	}

	insecurePath := filepath.Join(t.TempDir(), "auth.json")
	credential := credentials{Version: 1, Type: "oauth", AccessToken: testJWT(t, "private-account", "permissions"), RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "private-account"}
	if err := writeCredentials(insecurePath, credential); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecurePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(insecurePath); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("insecure credential read error = %v", err)
	}
}

func testJWT(t *testing.T, accountID, marker string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{
		"sub":                         "user",
		"marker":                      marker,
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	if err != nil {
		t.Fatal(err)
	}

	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func sha256Base64(value string) string {
	hash := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

type oauthRoundTripperFunc func(*http.Request) (*http.Response, error)

func (function oauthRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func writeTestJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
