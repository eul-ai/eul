package backend

import (
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type ModelDefaults struct {
	Main     string
	Fast     string
	Balanced string
}

type ModelMetadata struct {
	ContextWindow  int64
	ThinkingLevels []agent.ThinkingLevel
	FastMode       bool
}

func (metadata ModelMetadata) ClampThinkingLevel(level agent.ThinkingLevel) agent.ThinkingLevel {
	return agent.ClampThinkingLevel(level, metadata.ThinkingLevels)
}

type ModelNotSupportedError struct {
	Model           string
	AvailableModels []string
}

func (err *ModelNotSupportedError) Error() string {
	return fmt.Sprintf("model %q is not supported\navailable models:\n%s", err.Model, strings.Join(err.AvailableModels, "\n"))
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
