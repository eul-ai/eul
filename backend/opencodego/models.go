package opencodego

import (
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
	protocol              protocol
	contextWindow         int64
	thinkingLevels        []agent.ThinkingLevel
	thinkingMode          thinkingMode
	includeEncryptedState bool
	lowTextVerbosity      bool
}

var models = map[string]modelInfo{
	"grok-4.5": {
		protocol:              protocolResponses,
		contextWindow:         500_000,
		thinkingLevels:        []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh},
		thinkingMode:          thinkingEffort,
		includeEncryptedState: true,
	},
	"gpt-5.6-luna": {
		protocol:              protocolResponses,
		contextWindow:         1_050_000,
		thinkingLevels:        []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax},
		thinkingMode:          thinkingEffort,
		includeEncryptedState: true,
		lowTextVerbosity:      true,
	},
	"glm-5.3": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingEffort,
	},
	"glm-5.2": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingEffort,
	},
	"glm-5.1": {
		protocol:       protocolChatCompletions,
		contextWindow:  202_752,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"kimi-k3": {
		protocol:       protocolChatCompletions,
		contextWindow:  262_144,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"kimi-k2.7-code": {
		protocol:       protocolChatCompletions,
		contextWindow:  262_144,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"kimi-k2.6": {
		protocol:       protocolChatCompletions,
		contextWindow:  262_144,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"deepseek-v4-pro": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingEffort,
	},
	"deepseek-v4-flash": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingEffort,
	},
	"mimo-v2.5": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"mimo-v2.5-pro": {
		protocol:       protocolChatCompletions,
		contextWindow:  1_048_576,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"hy3": {
		protocol:       protocolChatCompletions,
		contextWindow:  256_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh},
		thinkingMode:   thinkingEffort,
	},
	"minimax-m3": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
		thinkingMode:   thinkingAdaptive,
	},
	"minimax-m2.7": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  204_800,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"minimax-m2.5": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  204_800,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh},
		thinkingMode:   thinkingFixed,
	},
	"qwen3.8-max": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingBudget,
	},
	"qwen3.7-max": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingBudget,
	},
	"qwen3.7-plus": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingBudget,
	},
	"qwen3.6-plus": {
		protocol:       protocolAnthropicMessages,
		contextWindow:  1_000_000,
		thinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh, agent.ThinkingMax},
		thinkingMode:   thinkingBudget,
	},
}

func metadataFor(model string) backend.ModelMetadata {
	info, ok := models[model]
	if !ok {
		return backend.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}
	return backend.ModelMetadata{
		ContextWindow:  info.contextWindow,
		ThinkingLevels: append([]agent.ThinkingLevel(nil), info.thinkingLevels...),
	}
}
