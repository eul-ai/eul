package builtins

import (
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex"
	"github.com/eul-ai/eul/backend/openrouter"
)

func NewRegistry() (*backend.Registry, error) {
	return backend.NewRegistry(
		codex.ID,
		codex.New(),
		openrouter.New(),
	)
}
