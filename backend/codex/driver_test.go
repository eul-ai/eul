package codex

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenChecksAuthenticationAndCreatesProvider(t *testing.T) {
	if _, err := open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("open accepted missing credentials")
	}

	home := t.TempDir()
	writeCredential(t, filepath.Join(home, "auth.json"), "access")
	opened, err := open(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opened.Provider.(provider); !ok {
		t.Fatalf("provider = %T", opened.Provider)
	}
}

func TestDefaultModelsAreSupported(t *testing.T) {
	defaults := New().Models
	opened := catalog()
	for _, model := range []string{defaults.Main, defaults.Fast, defaults.Balanced} {
		if metadata, ok := opened[model]; !ok || !metadata.FastMode || metadata.ContextWindow != 272_000 {
			t.Fatalf("model %q metadata = %+v", model, metadata)
		}
	}
}
