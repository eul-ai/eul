package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/eul-ai/eul/backend/testhttp"
)

func newTestServer(t *testing.T, handler http.Handler) *testhttp.Server {
	t.Helper()
	return testhttp.NewServer(handler)
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
