package backend

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type Models struct {
	Main     string `json:"main"`
	Fast     string `json:"fast"`
	Balanced string `json:"balanced"`
}

// WithDefaults replaces empty models with the provider defaults. Fast and
// balanced models fall back to the main model.
func (models Models) WithDefaults(defaults Models) Models {
	models.Main = cmp.Or(models.Main, defaults.Main)
	models.Fast = cmp.Or(models.Fast, defaults.Fast, models.Main)
	models.Balanced = cmp.Or(models.Balanced, defaults.Balanced, models.Main)
	return models
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
