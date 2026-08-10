package tool

import (
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	subagentFinalizeAfterDuration    = 5 * time.Minute
	subagentFinalizeAfterGenerations = 20
	subagentFinalizationPrompt       = "The subagent work budget has been reached. Do not call tools. Return a concise final answer containing the useful findings and conclusions established so far, and clearly identify any unfinished areas."
)

func NewSubagentFinalizationPolicy(onBegin func(agent.FinalizationReason)) agent.FinalizationPolicy {
	return agent.FinalizationPolicy{
		AfterDuration:    subagentFinalizeAfterDuration,
		AfterGenerations: subagentFinalizeAfterGenerations,
		Prompt:           subagentFinalizationPrompt,
		OnBegin:          onBegin,
	}
}
