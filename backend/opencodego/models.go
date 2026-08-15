package opencodego

import (
	"slices"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

type protocol uint8

const (
	protocolResponses protocol = iota + 1
	protocolChatCompletions
	protocolAnthropicMessages
)

type thinkingMode uint8

const (
	thinkingFixed thinkingMode = iota
	thinkingEffort
	thinkingBudget
	thinkingAdaptive
)

type modelInfo struct {
	protocol                  protocol
	contextWindow             int64
	thinkingLevels            []agent.ThinkingLevel
	thinkingMode              thinkingMode
	thinkingEfforts           map[agent.ThinkingLevel]string
	includeEncryptedState     bool
	lowTextVerbosity          bool
	serializeReasoningContent bool
}

type catalogProvider struct {
	ID     string                  `json:"id"`
	NPM    string                  `json:"npm"`
	Models map[string]catalogModel `json:"models"`
}

type catalogModel struct {
	ID               string                    `json:"id"`
	Reasoning        bool                      `json:"reasoning"`
	ReasoningOptions *[]catalogReasoningOption `json:"reasoning_options"`
	Limit            catalogLimit              `json:"limit"`
	Provider         catalogModelProvider      `json:"provider"`
}

type catalogReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values"`
}

type catalogLimit struct {
	Context int64 `json:"context"`
}

type catalogModelProvider struct {
	NPM string `json:"npm"`
}

func buildModels(catalog catalogProvider, live map[string]struct{}) map[string]modelInfo {
	models := make(map[string]modelInfo)
	for id := range live {
		model, ok := catalog.Models[id]
		if !ok || model.ID != id {
			continue
		}

		info, ok := buildModelInfo(catalog.NPM, id, model)
		if ok {
			models[id] = info
		}
	}
	return models
}

func buildModelInfo(defaultNPM, id string, model catalogModel) (modelInfo, bool) {
	if model.Limit.Context <= 0 {
		return modelInfo{}, false
	}

	npm := model.Provider.NPM
	if npm == "" {
		npm = defaultNPM
	}

	var selectedProtocol protocol
	switch npm {
	case "@ai-sdk/openai":
		selectedProtocol = protocolResponses
	case "@ai-sdk/openai-compatible":
		selectedProtocol = protocolChatCompletions
	case "@ai-sdk/anthropic":
		selectedProtocol = protocolAnthropicMessages
	default:
		return modelInfo{}, false
	}

	levels, mode, efforts, ok := modelThinking(model)
	if !ok {
		return modelInfo{}, false
	}
	switch selectedProtocol {
	case protocolResponses, protocolChatCompletions:
		if mode == thinkingBudget || mode == thinkingAdaptive {
			return modelInfo{}, false
		}
	case protocolAnthropicMessages:
		if mode == thinkingEffort {
			return modelInfo{}, false
		}
	}

	info := modelInfo{
		protocol:              selectedProtocol,
		contextWindow:         model.Limit.Context,
		thinkingLevels:        levels,
		thinkingMode:          mode,
		thinkingEfforts:       efforts,
		includeEncryptedState: selectedProtocol == protocolResponses,
	}
	switch id {
	case "gpt-5.6-luna":
		info.lowTextVerbosity = true
	case "deepseek-v4-pro", "deepseek-v4-flash":
		info.serializeReasoningContent = true
	}
	return info, true
}

func modelThinking(model catalogModel) ([]agent.ThinkingLevel, thinkingMode, map[agent.ThinkingLevel]string, bool) {
	if !model.Reasoning {
		return []agent.ThinkingLevel{agent.ThinkingOff}, thinkingFixed, nil, true
	}
	if model.ReasoningOptions == nil {
		return nil, thinkingFixed, nil, false
	}

	options := *model.ReasoningOptions
	for _, option := range options {
		if option.Type != "effort" {
			continue
		}

		efforts := make(map[agent.ThinkingLevel]string)
		for _, value := range option.Values {
			wireValue := "none"
			if value != nil {
				wireValue = *value
			}
			level := agent.ThinkingLevel(wireValue)
			if wireValue == "none" {
				level = agent.ThinkingOff
			}
			if !level.Valid() {
				continue
			}
			if _, exists := efforts[level]; !exists {
				efforts[level] = wireValue
			}
		}
		levels := orderedThinkingLevels(efforts)
		return levels, thinkingEffort, efforts, len(levels) > 0
	}

	for _, option := range options {
		if option.Type == "budget_tokens" {
			return []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax}, thinkingBudget, nil, true
		}
	}
	for _, option := range options {
		if option.Type == "toggle" {
			return []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh}, thinkingAdaptive, nil, true
		}
	}
	if len(options) == 0 {
		return []agent.ThinkingLevel{agent.ThinkingHigh}, thinkingFixed, nil, true
	}
	return nil, thinkingFixed, nil, false
}

func orderedThinkingLevels(efforts map[agent.ThinkingLevel]string) []agent.ThinkingLevel {
	levels := make([]agent.ThinkingLevel, 0, len(efforts))
	for _, level := range agent.ThinkingLevels() {
		if _, ok := efforts[level]; ok {
			levels = append(levels, level)
		}
	}
	return levels
}

func metadataFor(models map[string]modelInfo, model string) backend.ModelMetadata {
	info, ok := models[model]
	if !ok {
		return backend.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}
	return backend.ModelMetadata{
		ContextWindow:  info.contextWindow,
		ThinkingLevels: slices.Clone(info.thinkingLevels),
	}
}
