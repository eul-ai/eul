package tool

import (
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	subagentFinalizeAfterDuration    = 5 * time.Minute
	subagentFinalizeAfterTokens      = 200_000
	subagentFinalizeAfterGenerations = 20
	subagentFinalizationPrompt       = "The subagent work budget has been reached. Do not call tools. Return a concise final answer containing the useful findings and conclusions established so far, and clearly identify any unfinished areas."
)

func NewSubagentFinalizationPolicy(onBegin func()) agent.FinalizationPolicy {
	return agent.FinalizationPolicy{
		AfterDuration:    subagentFinalizeAfterDuration,
		AfterTokens:      subagentFinalizeAfterTokens,
		AfterGenerations: subagentFinalizeAfterGenerations,
		Prompt:           subagentFinalizationPrompt,
		OnBegin:          onBegin,
	}
}
