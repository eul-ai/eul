package subagent

import (
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	finalizeAfterDuration = 5 * time.Minute
	finalizationPrompt    = "The subagent work budget has been reached. Do not call tools. Return a concise final answer containing the useful findings and conclusions established so far, and clearly identify any unfinished areas."
)

func NewFinalizationPolicy(onBegin func()) agent.FinalizationPolicy {
	return agent.FinalizationPolicy{
		AfterDuration:    finalizeAfterDuration,
		AfterGenerations: generationLimit,
		Prompt:           finalizationPrompt,
		OnBegin: func(agent.FinalizationReason) {
			onBegin()
		},
	}
}
