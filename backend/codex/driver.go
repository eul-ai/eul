package codex

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/oauth"
)

const ID = "openai-codex"

func New() backend.Backend {
	return backend.Backend{
		ID:   ID,
		Name: "OpenAI Codex",
		Models: backend.Models{
			Main:     modelMain,
			Fast:     modelFast,
			Balanced: modelBalanced,
		},
		Open: open,
		Auth: func(home string) backend.Authenticator { return newManager(home) },
	}
}

func newManager(home string) *oauth.Manager {
	return oauth.NewManager(filepath.Join(home, "auth.json"))
}

func open(ctx context.Context, home string) (*backend.Runtime, error) {
	manager := newManager(home)
	if _, err := manager.Resolve(ctx); err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}

	return &backend.Runtime{
		Provider: newProvider(manager, nil),
		Usage: func(ctx context.Context) (backend.AccountUsage, error) {
			return loadUsage(ctx, manager, nil)
		},
		Catalog: catalog(),
	}, nil
}
