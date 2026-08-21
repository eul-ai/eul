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

const (
	reasoningOptionTypeEffort       = "effort"
	reasoningOptionTypeBudgetTokens = "budget_tokens"
	reasoningOptionTypeToggle       = "toggle"

	highThinkingBudgetTokens  = 16_000
	maxThinkingOutputHeadroom = 8_000
)

var (
	lowTextVerbosityModels = map[string]struct{}{
		"gpt-5.6-luna": {},
	}
	serializeReasoningContentModels = map[string]struct{}{
		"deepseek-v4-pro":   {},
		"deepseek-v4-flash": {},
	}
)

type thinkingMode uint8

const (
	thinkingFixed thinkingMode = iota
	thinkingEffort
	thinkingBudget
	thinkingAdaptive
)

type thinkingConfig struct {
	mode            thinkingMode
	levels          []agent.ThinkingLevel
	efforts         map[agent.ThinkingLevel]string
	maxBudgetTokens int
}

type modelInfo struct {
	contextWindow             int64
	thinking                  thinkingConfig
	protocol                  protocol
	maxOutputTokens           int
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
	Max    int       `json:"max"`
}

type catalogLimit struct {
	Context int64 `json:"context"`
	Output  int   `json:"output"`
}

type catalogModelProvider struct {
	NPM string `json:"npm"`
}

func modelNotSupportedError(model string, models map[string]modelInfo) error {
	available := make([]string, 0, len(models))
	for id := range models {
		available = append(available, id)
	}
	return backend.NewModelNotSupportedError("opencode go", model, available)
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
	if model.Limit.Context <= 0 || model.Limit.Output <= 0 {
		return modelInfo{}, false
	}

	npm := model.Provider.NPM
	if npm == "" {
		npm = defaultNPM
	}

	thinking, ok := modelThinking(model)
	if !ok {
		return modelInfo{}, false
	}

	info := modelInfo{
		contextWindow:   model.Limit.Context,
		thinking:        thinking,
		maxOutputTokens: model.Limit.Output,
	}
	switch npm {
	case "@ai-sdk/openai":
		if thinking.mode == thinkingBudget || thinking.mode == thinkingAdaptive {
			return modelInfo{}, false
		}
		info.protocol = protocolResponses
		_, info.lowTextVerbosity = lowTextVerbosityModels[id]
	case "@ai-sdk/openai-compatible":
		if thinking.mode == thinkingBudget || thinking.mode == thinkingAdaptive {
			return modelInfo{}, false
		}
		info.protocol = protocolChatCompletions
		_, info.serializeReasoningContent = serializeReasoningContentModels[id]
	case "@ai-sdk/anthropic":
		if thinking.mode == thinkingEffort {
			return modelInfo{}, false
		}
		info.protocol = protocolAnthropicMessages
	default:
		return modelInfo{}, false
	}

	return info, true
}

func modelThinking(model catalogModel) (thinkingConfig, bool) {
	if !model.Reasoning {
		return thinkingConfig{mode: thinkingFixed, levels: []agent.ThinkingLevel{agent.ThinkingOff}}, true
	}
	if model.ReasoningOptions == nil {
		return thinkingConfig{}, false
	}

	options := *model.ReasoningOptions
	for _, option := range options {
		if option.Type != reasoningOptionTypeEffort {
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
		return thinkingConfig{mode: thinkingEffort, levels: levels, efforts: efforts}, len(levels) > 0
	}

	for _, option := range options {
		if option.Type != reasoningOptionTypeBudgetTokens {
			continue
		}
		maximum := min(option.Max, model.Limit.Output-maxThinkingOutputHeadroom)
		if maximum <= highThinkingBudgetTokens {
			return thinkingConfig{}, false
		}
		return thinkingConfig{
			mode:            thinkingBudget,
			levels:          []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
			maxBudgetTokens: maximum,
		}, true
	}
	for _, option := range options {
		if option.Type == reasoningOptionTypeToggle {
			return thinkingConfig{
				mode:   thinkingAdaptive,
				levels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
			}, true
		}
	}
	if len(options) == 0 {
		return thinkingConfig{mode: thinkingFixed, levels: []agent.ThinkingLevel{agent.ThinkingHigh}}, true
	}
	return thinkingConfig{}, false
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
		ThinkingLevels: slices.Clone(info.thinking.levels),
	}
}
