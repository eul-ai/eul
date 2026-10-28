package oauth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func handlerTransport(handler http.HandlerFunc) http.RoundTripper {
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder.Result(), nil
	})
}

func newTestManager(path string, handler http.HandlerFunc) *Manager {
	manager := NewManager(path)
	manager.httpClient.Transport = handlerTransport(handler)
	return manager
}

func writeTestJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func readCredentials(path string) (credentials, error) {
	return (&credentialStore{path: path}).read()
}

func writeCredentials(path string, credential credentials) error {
	return (&credentialStore{path: path}).write(credential)
}
