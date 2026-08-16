package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/backend"
)

func TestOAuthHTTPRedirectBoundsAndCancellation(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		destinationCalls := 0
		destination := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls++ }))
		defer destination.Close()
		origin := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: origin.Client(), AuthBaseURL: origin.URL})
		err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "HTTP 307") || destinationCalls != 0 {
			t.Fatalf("error=%v destinationCalls=%d", err, destinationCalls)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", int(maxAuthResponseBytes)+1))
		}))
		defer server.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: server.Client(), AuthBaseURL: server.URL})
		err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
		if err == nil {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device", "user_code": "code", "interval": "1"})
		}))
		defer server.Close()
		manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: server.Client(), AuthBaseURL: server.URL})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := manager.LoginDevice(ctx, func(backend.DeviceCode) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}
