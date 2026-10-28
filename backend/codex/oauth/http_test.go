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
		manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Host == "destination.test" {
				destinationCalls++
				return
			}
			http.Redirect(writer, request, "https://destination.test/", http.StatusTemporaryRedirect)
		})
		err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "HTTP 307") || destinationCalls != 0 {
			t.Fatalf("error=%v destinationCalls=%d", err, destinationCalls)
		}
	})

	t.Run("bounded", func(t *testing.T) {
		manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", int(maxAuthResponseBytes)+1))
		})
		err := manager.LoginDevice(context.Background(), func(backend.DeviceCode) error { return nil })
		if err == nil {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		manager := newTestManager(filepath.Join(t.TempDir(), "auth.json"), func(writer http.ResponseWriter, _ *http.Request) {
			writeTestJSON(t, writer, map[string]any{"device_auth_id": "device", "user_code": "code", "interval": "1"})
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := manager.LoginDevice(ctx, func(backend.DeviceCode) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}
