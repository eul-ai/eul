package client

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
)

const (
	ModelGPT56Luna  = "gpt-5.6-luna"
	ModelGPT56Terra = "gpt-5.6-terra"
	ModelGPT56Sol   = "gpt-5.6-sol"
)

type responseReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

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
	agent.ThinkingOff:    "none",
	agent.ThinkingLow:    "low",
	agent.ThinkingMedium: "medium",
	agent.ThinkingHigh:   "high",
	agent.ThinkingXHigh:  "xhigh",
	agent.ThinkingMax:    "max",
}

var models = map[string]modelMetadata{
	ModelGPT56Luna:  {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
	ModelGPT56Terra: {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
	ModelGPT56Sol:   {contextWindow: 272_000, thinkingLevels: extendedThinkingLevelMap, fastMode: true},
}

type ModelMetadata struct {
	ContextWindow  int64
	ThinkingLevels []agent.ThinkingLevel
	FastMode       bool
}

func MetadataFor(model string) ModelMetadata {
	metadata, ok := models[model]
	if !ok {
		return ModelMetadata{ThinkingLevels: standardThinkingLevelMap.SupportedLevels()}
	}
	return ModelMetadata{
		ContextWindow:  metadata.contextWindow,
		ThinkingLevels: metadata.thinkingLevels.SupportedLevels(),
		FastMode:       metadata.fastMode,
	}
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

func (c *Client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	return compaction.ShouldCompact(
		request,
		usage,
		models[request.Model].contextWindow,
		c.responses.ShouldCompactState(request),
	)
}
