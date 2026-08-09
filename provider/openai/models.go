package openai

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
)

type modelMetadata struct {
	contextWindow  int64
	thinkingLevels agent.ThinkingLevelMap
}

var standardThinkingLevelMap = agent.ThinkingLevelMap{
	agent.ThinkingOff:     "none",
	agent.ThinkingMinimal: "minimal",
	agent.ThinkingLow:     "low",
	agent.ThinkingMedium:  "medium",
	agent.ThinkingHigh:    "high",
}

var extendedThinkingLevelMap = agent.ThinkingLevelMap{
	agent.ThinkingOff:     "none",
	agent.ThinkingMinimal: "minimal",
	agent.ThinkingLow:     "low",
	agent.ThinkingMedium:  "medium",
	agent.ThinkingHigh:    "high",
	agent.ThinkingXHigh:   "xhigh",
	agent.ThinkingMax:     "max",
}

var models = map[string]modelMetadata{
	"gpt-5.6-luna":  {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap},
	"gpt-5.6-terra": {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap},
	"gpt-5.6-sol":   {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap},
}

func contextWindow(model string) int64 {
	return models[model].contextWindow
}

func supportedThinkingLevels(model string) []agent.ThinkingLevel {
	return thinkingLevelMap(model).SupportedLevels()
}

func thinkingLevelMap(model string) agent.ThinkingLevelMap {
	if metadata, ok := models[model]; ok {
		return metadata.thinkingLevels
	}
	return standardThinkingLevelMap
}

type ReasoningSummary string

const (
	ReasoningSummaryAuto     ReasoningSummary = "auto"
	ReasoningSummaryConcise  ReasoningSummary = "concise"
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
	ReasoningSummaryNone     ReasoningSummary = "none"
)

func ParseReasoningSummary(value string) (ReasoningSummary, error) {
	if value == "" {
		return ReasoningSummaryAuto, nil
	}
	summary := ReasoningSummary(value)
	switch summary {
	case ReasoningSummaryAuto, ReasoningSummaryConcise, ReasoningSummaryDetailed, ReasoningSummaryNone:
		return summary, nil
	default:
		return "", fmt.Errorf("reasoning summary must be one of auto, concise, detailed, or none")
	}
}

func responseReasoningFor(model string, level agent.ThinkingLevel, summary ReasoningSummary) (*responseReasoning, error) {
	if summary == "" {
		summary = ReasoningSummaryAuto
	}
	if level == "" {
		level = agent.DefaultThinkingLevel
	}
	effort, ok := thinkingLevelMap(model)[level]
	if !ok {
		return nil, fmt.Errorf("thinking level %q is not supported by model %q", level, model)
	}

	reasoning := &responseReasoning{Effort: effort}
	if level != agent.ThinkingOff && summary != ReasoningSummaryNone {
		reasoning.Summary = string(summary)
	}
	return reasoning, nil
}

func (*Client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	if len(request.State) == 0 || usage.TotalTokens <= 0 {
		return false
	}

	metadata, ok := models[request.Model]
	if !ok {
		return false
	}
	limit := metadata.contextWindow * 9 / 10
	if usage.TotalTokens >= limit {
		return true
	}
	return estimateInputTokens(request.Inputs) >= limit-usage.TotalTokens
}

func estimateInputTokens(inputs []agent.Input) int64 {
	var total int64
	for _, input := range inputs {
		bytes := int64(len(input.Text))
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}
