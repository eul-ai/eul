package openrouter

import (
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type modelCatalog struct {
	Data []modelDescription `json:"data"`
}

type modelDescription struct {
	ID            string         `json:"id"`
	ContextLength int64          `json:"context_length"`
	Reasoning     modelReasoning `json:"reasoning"`
}

type modelReasoning struct {
	Mandatory        bool     `json:"mandatory"`
	SupportedEfforts []string `json:"supported_efforts"`
	DefaultEffort    string   `json:"default_effort"`
}

type modelMetadata struct {
	contextWindow        int64
	reasoning            bool
	thinkingLevels       []agent.ThinkingLevel
	defaultThinkingLevel agent.ThinkingLevel
}

func buildModels(catalog modelCatalog) map[string]modelMetadata {
	models := make(map[string]modelMetadata, len(catalog.Data))
	for _, description := range catalog.Data {
		metadata, ok := buildModelMetadata(description)
		if ok {
			models[description.ID] = metadata
		}
	}
	return models
}

func buildModelMetadata(description modelDescription) (modelMetadata, bool) {
	if strings.TrimSpace(description.ID) == "" || description.ContextLength < 0 {
		return modelMetadata{}, false
	}
	thinkingLevels, defaultThinkingLevel := thinkingMetadata(description.Reasoning)
	return modelMetadata{
		contextWindow:        description.ContextLength,
		reasoning:            len(description.Reasoning.SupportedEfforts) > 0,
		thinkingLevels:       thinkingLevels,
		defaultThinkingLevel: defaultThinkingLevel,
	}, true
}

func (metadata modelMetadata) backendMetadata() backend.ModelMetadata {
	levels := append([]agent.ThinkingLevel(nil), metadata.thinkingLevels...)
	if len(levels) == 0 {
		levels = []agent.ThinkingLevel{agent.ThinkingOff}
	}
	return backend.ModelMetadata{ContextWindow: metadata.contextWindow, ThinkingLevels: levels}
}

func (metadata modelMetadata) resolveThinkingLevel(level agent.ThinkingLevel) (agent.ThinkingLevel, bool) {
	if level == "" {
		level = metadata.defaultThinkingLevel
		if level == "" && !metadata.reasoning {
			level = agent.ThinkingOff
		}
	}

	if !metadata.reasoning && level == agent.ThinkingOff {
		return level, true
	}
	for _, candidate := range metadata.thinkingLevels {
		if candidate == level {
			return level, true
		}
	}
	return level, false
}

func (metadata modelMetadata) effortForResolvedLevel(level agent.ThinkingLevel) string {
	if level == agent.ThinkingOff {
		return "none"
	}
	return string(level)
}

func thinkingMetadata(reasoning modelReasoning) ([]agent.ThinkingLevel, agent.ThinkingLevel) {
	supported := make(map[agent.ThinkingLevel]bool, len(reasoning.SupportedEfforts)+1)
	if !reasoning.Mandatory {
		supported[agent.ThinkingOff] = true
	}
	for _, effort := range reasoning.SupportedEfforts {
		if level, ok := thinkingLevelForEffort(effort); ok {
			supported[level] = true
		}
	}

	levels := make([]agent.ThinkingLevel, 0, len(supported))
	for _, level := range agent.ThinkingLevels() {
		if supported[level] {
			levels = append(levels, level)
		}
	}

	defaultLevel, ok := thinkingLevelForEffort(reasoning.DefaultEffort)
	if !ok && !reasoning.Mandatory {
		defaultLevel = agent.ThinkingOff
	}
	return levels, defaultLevel
}

func thinkingLevelForEffort(effort string) (agent.ThinkingLevel, bool) {
	if effort == "none" {
		return agent.ThinkingOff, true
	}
	level := agent.ThinkingLevel(effort)
	return level, level.Valid()
}
