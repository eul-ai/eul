package api

import (
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
