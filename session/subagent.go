package session

import (
	"context"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/tool"
)

func (config config) subagentModel(profile tool.SubagentModelProfile) string {
	var model string
	switch profile {
	case tool.SubagentModelFast:
		model = config.subagentFastModel
	case tool.SubagentModelBalanced:
		model = config.subagentBalancedModel
	}
	if model == "" {
		return config.model
	}
	return model
}

func runChildAgent(
	ctx context.Context,
	backendRuntime backend.Runtime,
	newToolset toolsetFactory,
	config config,
	modelProfile tool.SubagentModelProfile,
	thinkingLevel agent.ThinkingLevel,
	task string,
	update func(tool.SubagentProgress),
) (agent.RunResult, error) {
	provider, err := backendRuntime.NewProvider()
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent provider: %w", err)
	}

	registry, err := newToolset(config.cwd, readOnlyToolAccess)
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent tools: %w", err)
	}
	child := agent.New(provider, registry, agent.Options{
		Model:               config.subagentModel(modelProfile),
		ThinkingLevel:       thinkingLevel,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
	})
	var liveUsage agent.Usage
	normalGenerations := 0
	finalizing := false
	policy := tool.NewSubagentFinalizationPolicy(func(reason agent.FinalizationReason) {
		finalizing = true
		update(tool.SubagentProgress{
			Usage:              liveUsage,
			Generations:        normalGenerations,
			Finalizing:         true,
			FinalizationReason: reason,
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
			update(tool.SubagentProgress{Usage: liveUsage, Generations: normalGenerations})
		}
		return nil
	}, policy)
	return result, finishRegistry(runErr, registry, "close subagent tools")
}
