package backend

import (
	"maps"
	"slices"
	"sync"

	"github.com/eul-ai/eul/agent"
)

type Models[T any] struct {
	provider string
	mu       sync.RWMutex
	byID     map[string]T
}

func NewModels[T any](provider string) *Models[T] {
	return &Models[T]{provider: provider}
}

func (models *Models[T]) Replace(byID map[string]T) {
	models.mu.Lock()
	models.byID = byID
	models.mu.Unlock()
}

func (models *Models[T]) Snapshot() *Models[T] {
	models.mu.RLock()
	defer models.mu.RUnlock()
	return &Models[T]{provider: models.provider, byID: models.byID}
}

func (models *Models[T]) Lookup(id string) (T, error) {
	models.mu.RLock()
	defer models.mu.RUnlock()
	model, ok := models.byID[id]
	if !ok {
		return model, NewModelNotSupportedError(models.provider, id, slices.Collect(maps.Keys(models.byID)))
	}
	return model, nil
}

func ThinkingEfforts(efforts []string) agent.ThinkingLevelMap {
	levels := make(agent.ThinkingLevelMap, len(efforts))
	for _, effort := range efforts {
		level := agent.ThinkingLevel(effort)
		if effort == "none" {
			level = agent.ThinkingOff
		}
		if _, exists := levels[level]; level.Valid() && !exists {
			levels[level] = effort
		}
	}
	return levels
}
