package codex

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

const (
	modelFast     = "gpt-5.6-luna"
	modelBalanced = "gpt-5.6-terra"
	modelMain     = "gpt-5.6-sol"
)

type model struct {
	contextWindow int64
	efforts       agent.ThinkingLevelMap
	fastMode      bool
}

var efforts = agent.ThinkingLevelMap{
	agent.ThinkingOff:    "none",
	agent.ThinkingLow:    "low",
	agent.ThinkingMedium: "medium",
	agent.ThinkingHigh:   "high",
	agent.ThinkingXHigh:  "xhigh",
	agent.ThinkingMax:    "max",
}

var models = map[string]model{
	modelFast:     {contextWindow: 272_000, efforts: efforts, fastMode: true},
	modelBalanced: {contextWindow: 272_000, efforts: efforts, fastMode: true},
	modelMain:     {contextWindow: 272_000, efforts: efforts, fastMode: true},
}

func modelMetadata(name string) backend.ModelMetadata {
	model := models[name]
	return backend.ModelMetadata{
		ContextWindow:  model.contextWindow,
		ThinkingLevels: model.efforts.SupportedLevels(),
		FastMode:       model.fastMode,
	}
}

func reasoningFor(model string, level agent.ThinkingLevel) (*responses.Reasoning, error) {
	effort, ok := models[model].efforts[level]
	if !ok {
		return nil, fmt.Errorf("thinking level %q is not supported by model %q", level, model)
	}

	reasoning := &responses.Reasoning{Effort: effort}
	if level != agent.ThinkingOff {
		reasoning.Summary = "auto"
	}
	return reasoning, nil
}
