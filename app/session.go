package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type sessionRunner interface {
	Run(context.Context, terminal.Options) (terminal.RunOutcome, error)
}

type agentSession struct {
	config          sessionSettings
	engine          *agent.Engine
	subagents       *subagent.Manager
	terminalOptions terminal.Options
	agentSettings   *agent.Settings
	persistence     *sessionPersistence
}

func newSessionTools(
	config resolvedConfig,
	env environment,
	provider agent.Provider,
	metadata map[subagent.Profile]backend.ModelMetadata,
	settings *agent.Settings,
	authorizeNetwork tool.NetworkAuthorizer,
	goal *agent.Goal,
) ([]agent.Tool, *subagent.Manager) {
	newToolset := env.newToolset
	if newToolset == nil {
		newToolset = buildToolset
	}
	runner := childRunner{
		provider:         provider,
		newToolset:       newToolset,
		authorizeNetwork: authorizeNetwork,
		config:           config,
		models:           metadata,
		settings:         settings,
	}
	manager := subagent.NewManager(runner.run)
	thinkingLevels := make(map[subagent.Profile][]agent.ThinkingLevel, len(metadata))
	for profile, model := range metadata {
		thinkingLevels[profile] = model.ThinkingLevels
	}
	tools := newToolset(
		config.cwd,
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

func newAgentSessionComponents(
	config resolvedConfig,
	env environment,
	runtime *backend.Runtime,
	noSandbox bool,
) (*agentSession, terminalOptionsBuilder, error) {
	metadata, err := resolveModelMetadata(runtime, config.models)
	if err != nil {
		return nil, terminalOptionsBuilder{}, err
	}
	main := metadata[subagent.ProfileMain]
	if config.fastMode && !main.FastMode {
		return nil, terminalOptionsBuilder{}, fmt.Errorf("fast mode is unavailable for model %q", config.models.Main)
	}
	warnings := append([]string(nil), config.warnings...)
	authorizeNetwork, permissionRequests := newNetworkPermissionBroker(noSandbox)
	settings := agent.NewSettings(main.ClampThinkingLevel(config.thinkingLevel), config.fastMode)

	goal := &agent.Goal{}
	tools, subagents := newSessionTools(config, env, runtime.Provider, metadata, settings, authorizeNetwork, goal)
	options := engineOptions(config, config.models.Main, settings)
	options.Checkpointing = true
	options.Inbox = tool.NewSubagentInbox(subagents)
	options.Goal = goal
	engine := agent.New(runtime.Provider, tools, options)
	session := &agentSession{
		config:        config.sessionSettings,
		engine:        engine,
		subagents:     subagents,
		agentSettings: settings,
	}
	terminalOptions := terminalOptionsBuilder{
		config:             config,
		runtime:            env,
		metadata:           main,
		warnings:           warnings,
		loadUsage:          runtime.Usage,
		subagents:          subagents,
		permissionRequests: permissionRequests,
		engine:             engine,
		settings:           settings,
		goal:               goal,
	}
	return session, terminalOptions, nil
}

func (session *agentSession) settings() sessionSettings {
	settings := session.config
	settings.thinkingLevel, settings.fastMode = session.agentSettings.Snapshot()
	return settings
}

func (session *agentSession) run(ctx context.Context, runner sessionRunner) (terminal.RunOutcome, error) {
	outcome, runErr := runner.Run(ctx, session.terminalOptions)
	return outcome, session.finish(runErr)
}

func (session *agentSession) finish(runErr error) error {
	if session.persistence != nil {
		session.persistence.stop()
	}
	session.subagents.Close()
	var finalCheckpointErr error
	if session.persistence != nil {
		finalCheckpointErr = session.persistence.saveFinal()
	}
	var persistenceErr error
	if session.persistence != nil {
		persistenceErr = session.persistence.close()
	}
	if persistenceErr != nil {
		persistenceErr = fmt.Errorf("close session: %w", persistenceErr)
	}
	return errors.Join(runErr, finalCheckpointErr, persistenceErr)
}
