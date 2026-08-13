package client

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
)

const (
	ModelGPT56Luna  = "gpt-5.6-luna"
	ModelGPT56Terra = "gpt-5.6-terra"
	ModelGPT56Sol   = "gpt-5.6-sol"
)

type modelMetadata struct {
	contextWindow  int64
	thinkingLevels agent.ThinkingLevelMap
	fastMode       bool
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
	ModelGPT56Luna:  {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
	ModelGPT56Terra: {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
	ModelGPT56Sol:   {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
}

func contextWindow(model string) int64 {
	return models[model].contextWindow
}

func SupportedThinkingLevels(model string) []agent.ThinkingLevel {
	return thinkingLevelMap(model).SupportedLevels()
}

func ContextWindow(model string) int64 {
	return contextWindow(model)
}

func FastModeAvailable(model string) bool {
	return models[model].fastMode
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

func (*Client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return contextLimitError(err)
}

func (c *Client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	if len(request.State) == 0 {
		return false
	}
	if c.maxStateBytes > 0 {
		if _, _, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes()); err != nil {
			withoutState := request
			withoutState.State = nil
			_, _, inputErr := buildCreateRequestWithLimit(withoutState, c.maxStateBytes, c.generationStateBytes())
			return inputErr == nil
		}
	}
	if usage.TotalTokens <= 0 {
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
		textBytes := len(input.Text)
		if input.Kind == agent.InputUser {
			textBytes = len(input.PlainText())
		}
		bytes := int64(textBytes)
		total += bytes / 4
		if bytes%4 != 0 {
			total++
		}
	}
	return total
}
