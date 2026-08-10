package builtin

import (
	"github.com/eul-ai/eul/backend"
	openai "github.com/eul-ai/eul/backend/openai"
)

func New() *backend.Registry {
	registry, err := backend.NewRegistry(openai.ID, openai.New())
	if err != nil {
		panic(err)
	}
	return registry
}
