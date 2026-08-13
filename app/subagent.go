package app

import (
	"context"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
)

func (models modelSet) forProfile(profile subagent.Profile) string {
	switch profile {
	case subagent.ProfileFast:
		return models.fast
	case subagent.ProfileBalanced:
		return models.balanced
	case subagent.ProfilePowerful:
		return models.primary
	default:
		return models.primary
	}
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
	result, runErr := child.Run(ctx, task, func(event agent.Event) error {
		switch event.Kind {
		case agent.EventCompactionEnd, agent.EventContextUsage:
			liveUsage.InputTokens += event.Usage.InputTokens
			liveUsage.OutputTokens += event.Usage.OutputTokens
			liveUsage.TotalTokens += event.Usage.TotalTokens
			update(subagent.Progress{Usage: liveUsage})
		}
		return nil
	})
	return result, runErr
}
