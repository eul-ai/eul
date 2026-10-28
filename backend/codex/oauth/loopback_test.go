package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

func TestBrowserCallbackRejectsWrongStateThenAcceptsValidState(t *testing.T) {
	access := testJWT(t, "account", "state")
	manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh", "expires_in": 3600})
	})
	err := manager.LoginBrowser(context.Background(), func(raw string) error {
		parsed, _ := url.Parse(raw)
		redirect := parsed.Query().Get("redirect_uri")
		wrong, err := requestCallback(redirect + "?code=secret-code&state=wrong")
		if err != nil {
			return err
		}
		wrong.Body.Close()
		if wrong.StatusCode != http.StatusBadRequest {
			t.Errorf("wrong-state status = %d", wrong.StatusCode)
		}
		valid, err := requestCallback(redirect + "?code=valid-code&state=" + url.QueryEscape(parsed.Query().Get("state")))
		if err != nil {
			return err
		}
		valid.Body.Close()
		return nil
	})
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
		if callback.err == nil {
			t.Fatalf("callback result = %+v", callback)
		}
	default:
		t.Fatal("missing-code callback did not finish the login")
	}
}
