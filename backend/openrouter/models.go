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
}

type modelMetadata struct {
	contextWindow int64
	efforts       agent.ThinkingLevelMap
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
	metadata := modelMetadata{contextWindow: description.ContextLength}
	if len(description.Reasoning.SupportedEfforts) > 0 {
		metadata.efforts = backend.ThinkingEfforts(description.Reasoning.SupportedEfforts)
		if !description.Reasoning.Mandatory {
			metadata.efforts[agent.ThinkingOff] = "none"
		}
	}
	return metadata, true
}

func (metadata modelMetadata) backendMetadata() backend.ModelMetadata {
	levels := metadata.efforts.SupportedLevels()
	if len(levels) == 0 {
		levels = []agent.ThinkingLevel{agent.ThinkingOff}
	}
	return backend.ModelMetadata{ContextWindow: metadata.contextWindow, ThinkingLevels: levels}
}
