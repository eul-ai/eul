package backend

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestRegistrySelectsDefaultAndExplicitProviders(t *testing.T) {
	registry := Registry{{ID: "first", Name: "First"}, {ID: "second", Name: "Second"}}
	for id, want := range map[string]string{"": "first", "first": "first", "second": "second"} {
		selected, err := registry.Lookup(id)
		if err != nil {
			t.Fatalf("lookup %q: %v", id, err)
		}
		if selected.ID != want {
			t.Fatalf("lookup %q = %q, want %q", id, selected.ID, want)
		}
	}
	if _, err := registry.Lookup("missing"); err == nil {
		t.Fatal("missing provider was accepted")
	}
}

func TestRuntimeModelReportsAvailableModels(t *testing.T) {
	runtime := &Runtime{Catalog: map[string]ModelMetadata{"zeta": {}, "alpha": {ContextWindow: 1}}}
	if metadata, err := runtime.Model("alpha"); err != nil || metadata.ContextWindow != 1 {
		t.Fatalf("Model() = %+v, %v", metadata, err)
	}

	_, err := runtime.Model("unknown")
	var unsupported *ModelNotSupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Model() error = %v", err)
	}
	if unsupported.Model != "unknown" || !slices.Equal(unsupported.AvailableModels, []string{"alpha", "zeta"}) {
		t.Fatalf("Model() error = %#v", unsupported)
	}
	if !strings.Contains(err.Error(), "available models:\nalpha\nzeta") {
		t.Fatalf("Model() error = %q", err)
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"openai-codex", "provider.v2", "a_1"} {
		if !ValidID(id) {
			t.Fatalf("valid ID %q was rejected", id)
		}
	}
	for _, id := range []string{"", "-provider", "Provider", "provider name", "provider/child"} {
		if ValidID(id) {
			t.Fatalf("invalid ID %q was accepted", id)
		}
	}
}
