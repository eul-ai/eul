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
	engine          *agent.Engine
	tools           []agent.Tool
	subagents       *subagent.Manager
	backendRuntime  backend.Runtime
	terminalOptions terminal.Options
	settings        *agent.Settings
	persistence     *sessionPersistence
}

func newSessionTools(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	metadata sessionModelMetadata,
	settings *agent.Settings,
	authorizeNetwork tool.NetworkAuthorizer,
	goal *agent.Goal,
) ([]agent.Tool, *subagent.Manager) {
	newToolset := env.newToolset
	if newToolset == nil {
		newToolset = buildToolset
	}
	runner := childRunner{
		backendRuntime:   backendRuntime,
		newToolset:       newToolset,
		authorizeNetwork: authorizeNetwork,
		config:           config,
		models:           metadata.subagent,
		settings:         settings,
	}
	manager := subagent.NewManager(runner.run)
	thinkingLevels := make(map[subagent.Profile][]agent.ThinkingLevel, len(metadata.subagent))
	for profile, model := range metadata.subagent {
		thinkingLevels[profile] = model.ThinkingLevels
	}
	tools := newToolset(
		config.cwd,
		fullToolAccess,
		config.noSandbox,
		authorizeNetwork,
		tool.NewLaunchSubagents(manager, thinkingLevels, settings),
		tool.NewWaitForSubagent(manager),
		tool.NewCancelSubagents(manager),
		tool.NewUpdateGoal(goal),
	)
	return tools, manager
}

func newAgentSession(config resolvedConfig, env environment, backendRuntime backend.Runtime) (*agentSession, error) {
	session, options, err := newAgentSessionComponents(config, env, backendRuntime, false)
	if err != nil {
		return nil, err
	}
	session.terminalOptions = options.options()
	return session, nil
}

func validateRuntimeModels(backendRuntime backend.Runtime, models modelSet) error {
	validator, ok := backendRuntime.(backend.ModelValidator)
	if !ok {
		return nil
	}

	validated := make(map[string]struct{}, 3)
	for _, model := range []struct {
		name  string
		value string
	}{
		{name: "main", value: models.main},
		{name: "fast", value: models.fast},
		{name: "balanced", value: models.balanced},
	} {
		if _, ok := validated[model.value]; ok {
			continue
		}
		validated[model.value] = struct{}{}
		if err := validator.ValidateModel(model.value); err != nil {
			return fmt.Errorf("%s model: %w", model.name, err)
		}
	}
	return nil
}

func newAgentSessionComponents(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	checkpointing bool,
) (*agentSession, terminalOptionsBuilder, error) {
	if err := validateRuntimeModels(backendRuntime, config.models); err != nil {
		return nil, terminalOptionsBuilder{}, errors.Join(err, closeBackendRuntime(backendRuntime))
	}

	provider, err := backendRuntime.NewProvider()
	if err != nil {
		return nil, terminalOptionsBuilder{}, errors.Join(fmt.Errorf("configure provider: %w", err), closeBackendRuntime(backendRuntime))
	}
	metadata := resolveSessionModelMetadata(backendRuntime, config)
	if config.fastMode && !metadata.main.FastMode {
		return nil, terminalOptionsBuilder{}, errors.Join(fmt.Errorf("fast mode is unavailable for model %q", config.models.main), closeBackendRuntime(backendRuntime))
	}
	loadUsage := runtimeUsageLoader(backendRuntime)
	warnings := append([]string(nil), config.warnings...)
	authorizeNetwork, permissionRequests := newNetworkPermissionBroker(config.noSandbox)
	settings := agent.NewSettings(metadata.thinkingLevel, config.fastMode)

	goal := &agent.Goal{}
	tools, subagents := newSessionTools(config, env, backendRuntime, metadata, settings, authorizeNetwork, goal)
	options := engineOptions(config, config.models.main, settings)
	options.Checkpointing = checkpointing
	options.Inbox = tool.NewSubagentInbox(subagents)
	options.Goal = goal
	engine := agent.New(provider, tools, options)
	session := &agentSession{
		engine:         engine,
		tools:          tools,
		subagents:      subagents,
		backendRuntime: backendRuntime,
		settings:       settings,
	}
	terminalOptions := terminalOptionsBuilder{
		config:             config,
		runtime:            env,
		metadata:           metadata,
		warnings:           warnings,
		loadUsage:          loadUsage,
		subagents:          subagents,
		permissionRequests: permissionRequests,
		engine:             engine,
		settings:           settings,
		goal:               goal,
	}
	return session, terminalOptions, nil
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
	backendErr := closeBackendRuntime(session.backendRuntime)
	session.backendRuntime = nil
	return errors.Join(runErr, finalCheckpointErr, persistenceErr, backendErr)
}

func closeBackendRuntime(backendRuntime backend.Runtime) error {
	if backendRuntime == nil {
		return nil
	}
	if err := backendRuntime.Close(); err != nil {
		return fmt.Errorf("close backend: %w", err)
	}
	return nil
}
