package app

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

const childAgentInstructions = "This is a read-only research task. Inspect and report findings, but do not modify files. The main agent will make any edits."

func modelForProfile(models backend.Models, profile subagent.Profile) string {
	switch profile {
	case subagent.ProfileFast:
		return models.Fast
	case subagent.ProfileBalanced:
		return models.Balanced
	default:
		return models.Main
	}
}

type childRunner struct {
	provider         agent.Provider
	authorizeNetwork tool.NetworkAuthorizer
	config           resolvedConfig
	models           map[subagent.Profile]backend.ModelMetadata
	settings         *agent.Settings
}

func (runner childRunner) run(ctx context.Context, task subagent.Task, progress func(agent.Usage)) (string, error) {
	tools := buildToolset(runner.config.cwd, subagentToolAccess, runner.authorizeNetwork)
	_, fastMode := runner.settings.Snapshot()
	fastMode = fastMode && runner.models[task.ModelProfile].FastMode
	child := agent.New(runner.provider, tools, agent.Options{
		Model:        modelForProfile(runner.config.models, task.ModelProfile),
		Settings:     agent.NewSettings(task.ThinkingLevel, fastMode),
		Instructions: systemPrompt(runner.config) + "\n\n" + childAgentInstructions,
	})

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
