package codex

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eul-ai/eul/backend"
)

func TestDriverOpensRuntimeWithAuthenticationAndProviderCreation(t *testing.T) {
	home := t.TempDir()
	writeCredential(t, filepath.Join(home, "auth.json"), "access")

	backendRuntime, err := New().Open(backend.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	checker, ok := backendRuntime.(backend.CredentialChecker)
	if !ok {
		t.Fatal("configured backend does not check credentials")
	}
	if err := checker.CheckCredentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	created, err := backendRuntime.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := created.(provider); !ok {
		t.Fatalf("provider = %T", created)
	}
	if err := backendRuntime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDriverModelDefaultsAreSupported(t *testing.T) {
	defaults := New().Descriptor().DefaultModels
	configured := &runtime{}
	for _, model := range []string{defaults.Main, defaults.Fast, defaults.Balanced} {
		if err := configured.ValidateModel(model); err != nil {
			t.Fatal(err)
		}
		if metadata := configured.ModelMetadata(model); !metadata.FastMode || metadata.ContextWindow != 272_000 {
			t.Fatalf("model %q metadata = %+v", model, metadata)
		}
	}

	err := configured.ValidateModel("unknown")
	var unsupported *backend.ModelNotSupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("ValidateModel() error = %v", err)
	}
	if unsupported.Model != "unknown" || !slices.Equal(unsupported.AvailableModels, []string{modelFast, modelMain, modelBalanced}) {
		t.Fatalf("ValidateModel() error = %#v", unsupported)
	}
}
