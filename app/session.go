package app

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

func (launcher *launcher) newSession(config resolvedConfig, runtime *backend.Runtime, handle *session.Handle, prompt string) (*session.Session, error) {
	metadata, err := resolveModelMetadata(runtime, config.Models)
	if err != nil {
		return nil, err
	}
	main := metadata[subagent.ProfileMain]
	if config.FastMode && !main.FastMode {
		return nil, fmt.Errorf("fast mode is unavailable for model %q", config.Models.Main)
	}

	settings := agent.NewSettings(main.ClampThinkingLevel(config.ThinkingLevel), config.FastMode)
	goal := &agent.Goal{}
	provider, usage := meterSession(runtime)
	conversation, err := session.New(session.Config{
		Handle:        handle,
		Store:         launcher.store,
		History:       launcher.history,
		AgentSettings: settings,
		Goal:          goal,
		Model:         main,
		Usage:         usage,
		Skills:        config.skills,
		Warnings:      config.warnings,
		Prompt:        prompt,
	})
	if err != nil {
		return nil, err
	}

	var authorizeNetwork tool.NetworkAuthorizer
	if !launcher.noSandbox {
		authorizeNetwork = conversation.AuthorizeNetwork
	}
	record := handle.Record()
	tools, subagents := newSessionTools(config, provider, metadata, settings, authorizeNetwork, goal, record.Subagent)
	conversation.Start(agent.New(provider, tools, agent.Options{
		Model:         config.Models.Main,
		Instructions:  systemPrompt(config),
		SessionID:     record.ID,
		Checkpoint:    record.Agent,
		Checkpointing: true,
		Inbox:         tool.NewSubagentInbox(subagents),
		Settings:      settings,
		Goal:          goal,
	}), subagents)
	return conversation, nil
}

func newSessionTools(
	config resolvedConfig,
	provider agent.Provider,
	metadata map[subagent.Profile]backend.ModelMetadata,
	settings *agent.Settings,
	authorizeNetwork tool.NetworkAuthorizer,
	goal *agent.Goal,
	checkpoint subagent.Checkpoint,
) ([]agent.Tool, *subagent.Manager) {
	runner := childRunner{
		provider:         provider,
		authorizeNetwork: authorizeNetwork,
		config:           config,
		models:           metadata,
		settings:         settings,
	}
	manager := subagent.NewManager(runner.run, checkpoint)
	thinkingLevels := make(map[subagent.Profile][]agent.ThinkingLevel, len(metadata))
	for profile, model := range metadata {
		thinkingLevels[profile] = model.ThinkingLevels
	}
	tools := buildToolset(
		config.WorkingDirectory,
		fullToolAccess,
		authorizeNetwork,
		tool.NewLaunchSubagents(manager, thinkingLevels, settings),
		tool.NewWaitForSubagent(manager),
		tool.NewCancelSubagents(manager),
		tool.NewUpdateGoal(goal),
	)
	return tools, manager
}

func resolveModelMetadata(runtime *backend.Runtime, models backend.Models) (map[subagent.Profile]backend.ModelMetadata, error) {
	metadata := make(map[subagent.Profile]backend.ModelMetadata, 3)
	for _, profile := range []subagent.Profile{subagent.ProfileMain, subagent.ProfileFast, subagent.ProfileBalanced} {
		model, err := runtime.Model(modelForProfile(models, profile))
		if err != nil {
			return nil, fmt.Errorf("%s model: %w", profile, err)
		}
		metadata[profile] = model
	}
	return metadata, nil
}
