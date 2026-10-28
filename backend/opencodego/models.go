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
	efforts         agent.ThinkingLevelMap
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

		values := make([]string, len(option.Values))
		for index, value := range option.Values {
			values[index] = "none"
			if value != nil {
				values[index] = *value
			}
		}
		efforts := backend.ThinkingEfforts(values)
		levels := efforts.SupportedLevels()
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

func metadataFor(models *backend.Models[modelInfo], model string) backend.ModelMetadata {
	info, err := models.Lookup(model)
	if err != nil {
		return backend.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}
	return backend.ModelMetadata{
		ContextWindow:  info.contextWindow,
		ThinkingLevels: slices.Clone(info.thinking.levels),
	}
}
