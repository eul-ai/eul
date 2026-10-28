package app

import (
	"context"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

func (models modelSet) forProfile(profile subagent.Profile) string {
	switch profile {
	case subagent.ProfileFast:
		return models.fast
	case subagent.ProfileBalanced:
		return models.balanced
	case subagent.ProfileMain:
		return models.main
	default:
		return models.main
	}
}

type childRunner struct {
	backendRuntime   backend.Runtime
	newToolset       toolsetFactory
	authorizeNetwork tool.NetworkAuthorizer
	config           resolvedConfig
	models           map[subagent.Profile]backend.ModelMetadata
	settings         *agent.Settings
}

func (runner childRunner) run(ctx context.Context, task subagent.Task, progress func(agent.Usage)) (string, error) {
	provider, err := runner.backendRuntime.NewProvider()
	if err != nil {
		return "", fmt.Errorf("configure subagent provider: %w", err)
	}

	tools := runner.newToolset(runner.config.cwd, subagentToolAccess, runner.authorizeNetwork)
	_, fastMode := runner.settings.Snapshot()
	fastMode = fastMode && runner.models[task.ModelProfile].FastMode
	child := agent.New(provider, tools, childEngineOptions(runner.config, task.ModelProfile, task.ThinkingLevel, fastMode))

	var usage agent.Usage
	result, err := child.Run(ctx, []agent.ContentPart{{Kind: agent.ContentPartText, Text: task.Prompt}}, func(event agent.Event) error {
		switch event.Kind {
		case agent.EventCompactionEnd, agent.EventContextUsage:
			usage.InputTokens += event.Usage.InputTokens
			usage.OutputTokens += event.Usage.OutputTokens
			usage.TotalTokens += event.Usage.TotalTokens
			progress(usage)
		}
		return nil
	})
	return result.Text, err
}
