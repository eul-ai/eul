package oauth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/eul-ai/eul/backend/testhttp"
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

func newTestServer(t *testing.T, handler http.Handler) *testhttp.Server {
	t.Helper()
	return testhttp.NewServer(handler)
}

func readCredentials(path string) (credentials, error) {
	return (&credentialStore{path: path, sleep: sleepContext}).read()
}

func writeCredentials(path string, credential credentials) error {
	return (&credentialStore{path: path, sleep: sleepContext}).write(credential)
}
