package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	server := newUnstartedTestServer(t, handler)
	server.Start()
	return server
}

func newUnstartedTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Skipf("local listeners are unavailable: %v", err)
	}
	return &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
}

type testTokenSourceFunc func(context.Context) (Credential, error)

func (function testTokenSourceFunc) Token(ctx context.Context) (Credential, error) {
	return function(ctx)
}

func testTokenSource(token string) TokenSource {
	return testTokenSourceFunc(func(context.Context) (Credential, error) {
		return Credential{AccessToken: token, AccountID: "account"}, nil
	})
}

func newTestClient(t *testing.T, token, baseURL string, options Options) *Client {
	t.Helper()
	options.BaseURL = baseURL
	client, err := New(testTokenSource(token), options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeCompactJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}
