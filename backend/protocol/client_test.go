package protocol

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewCopiesInjectedClientBeforeApplyingPolicy(t *testing.T) {
	redirect := func(*http.Request, []*http.Request) error { return nil }
	transport := http.DefaultTransport
	injected := &http.Client{Transport: transport, CheckRedirect: redirect}

	owned := New(Options{Endpoint: "https://example.com/responses", HTTPClient: injected}).config.HTTPClient
	if owned == injected || owned.Transport != transport || owned.Timeout != 0 {
		t.Fatalf("owned client=%p injected=%p transport=%T timeout=%s", owned, injected, owned.Transport, owned.Timeout)
	}
	if injected.Timeout != 0 || injected.CheckRedirect == nil {
		t.Fatalf("injected client was mutated: timeout=%s redirect missing=%t", injected.Timeout, injected.CheckRedirect == nil)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err := owned.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("owned redirect policy error = %v", err)
	}
	if err := injected.CheckRedirect(request, nil); err != nil {
		t.Fatalf("injected redirect policy changed: %v", err)
	}
}
