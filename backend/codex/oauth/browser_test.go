package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBrowserLoginPKCEAndCredentialStorage(t *testing.T) {
	const accountID = "account-123"
	access := testJWT(t, accountID, "one")
	var mu sync.Mutex
	var authorize url.Values
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/oauth/token" {
			t.Errorf("unexpected auth path %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		mu.Lock()
		captured := authorize
		mu.Unlock()
		hashVerifier := sha256Base64(request.Form.Get("code_verifier"))
		if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "authorization-code" || request.Form.Get("redirect_uri") != captured.Get("redirect_uri") || hashVerifier != captured.Get("code_challenge") {
			t.Errorf("invalid exchange form: %v", request.Form)
		}
		writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh-one", "expires_in": 600, "id_token": "ignored"})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "private", "auth.json")
	callbackListener := newPipeListener()
	manager := NewManager(path, Options{HTTPClient: server.Client(), AuthBaseURL: server.URL, CallbackAddress: "127.0.0.1:0"})
	manager.listen = func(string, string) (net.Listener, error) { return callbackListener, nil }
	err := manager.LoginBrowser(context.Background(), func(raw string) error {
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
		response, err := requestCallback(callbackListener, callback)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != http.StatusOK {
			t.Errorf("callback status = %d", response.StatusCode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	credential, err := readCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != access || credential.RefreshToken != "refresh-one" || credential.AccountID != accountID {
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
}

func sha256Base64(value string) string {
	hash := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
