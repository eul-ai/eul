package interactive

import (
	"context"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/tool/subagent"
)

func (models modelSelection) subagent(profile subagent.Profile) string {
	var selected string
	switch profile {
	case subagent.ProfileFast:
		selected = models.fast
	case subagent.ProfileBalanced:
		selected = models.balanced
	}
	if selected == "" {
		return models.main
	}
	return selected
}

func runChildAgent(
	ctx context.Context,
	backendRuntime backend.Runtime,
	newToolset toolsetFactory,
	config resolvedConfig,
	modelProfile subagent.Profile,
	thinkingLevel agent.ThinkingLevel,
	fastMode bool,
	task string,
	update func(subagent.Progress),
) (agent.RunResult, error) {
	provider, err := backendRuntime.NewProvider()
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent provider: %w", err)
	}

	registry, err := newToolset(config.cwd, readOnlyToolAccess, config.noSandbox, nil)
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent tools: %w", err)
	}
	child := agent.New(provider, registry, childEngineOptions(config, modelProfile, thinkingLevel, fastMode))
	var liveUsage agent.Usage
	normalGenerations := 0
	finalizing := false
	policy := subagent.NewFinalizationPolicy(func() {
		finalizing = true
		update(subagent.Progress{
			Usage:       liveUsage,
			Generations: normalGenerations,
			Finalizing:  true,
		})
	})
	result, runErr := child.RunWithFinalization(ctx, task, func(event agent.Event) error {
		switch event.Kind {
		case agent.EventCompactionEnd, agent.EventContextUsage:
			liveUsage.InputTokens += event.Usage.InputTokens
			liveUsage.OutputTokens += event.Usage.OutputTokens
			liveUsage.TotalTokens += event.Usage.TotalTokens
			if event.Kind == agent.EventContextUsage && !finalizing {
				normalGenerations++
			}
			update(subagent.Progress{Usage: liveUsage, Generations: normalGenerations})
		}
		return nil
	}, policy)
	return result, finishRegistry(runErr, registry, "close subagent tools")
}
