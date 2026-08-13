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

type sessionToolset struct {
	registry        *tool.Registry
	subagents       *subagent.Manager
	subagentUpdates <-chan subagent.Status
}

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	subagents       *subagent.Manager
	backendRuntime  backend.Runtime
	terminalOptions terminal.Options
	settings        *agent.Settings
	persistence     *sessionPersistence
}

func newSessionToolset(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	metadata sessionModelMetadata,
	settings *agent.Settings,
	authorizeNetwork tool.NetworkAuthorizer,
	completeGoal func() error,
) (sessionToolset, error) {
	newToolset := env.newToolset
	if newToolset == nil {
		newToolset = buildToolset
	}
	manager := subagent.NewManager(subagent.Config{
		Runner: subagent.RunFunc(func(ctx context.Context, request subagent.RunRequest, update func(subagent.Progress)) (agent.RunResult, error) {
			_, fastMode := settings.Snapshot()
			enabled := fastMode && metadata.subagent[request.Profile].FastMode
			return runChildAgent(ctx, backendRuntime, newToolset, config, request.Profile, request.ThinkingLevel, enabled, request.Task, update)
		}),
		SupportedThinkingLevels: func(profile subagent.Profile) []agent.ThinkingLevel {
			return metadata.subagent[profile].ThinkingLevels
		},
	})
	registry, err := newToolset(
		config.cwd,
		fullToolAccess,
		config.noSandbox,
		authorizeNetwork,
		tool.NewSubagent(manager),
		tool.NewSubagentWait(manager),
		tool.NewSubagentCancel(manager),
		tool.NewUpdateGoal(completeGoal),
	)
	if err != nil {
		return sessionToolset{}, errors.Join(fmt.Errorf("configure tools: %w", err), manager.Close())
	}
	return sessionToolset{registry: registry, subagents: manager, subagentUpdates: manager.StatusChanges()}, nil
}

func newAgentSession(config resolvedConfig, env environment, backendRuntime backend.Runtime) (*agentSession, error) {
	session, options, err := newAgentSessionComponents(config, env, backendRuntime, false)
	if err != nil {
		return nil, err
	}
	session.terminalOptions = options.options()
	return session, nil
}

func newAgentSessionComponents(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	checkpointing bool,
) (*agentSession, terminalOptionsBuilder, error) {
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

	var engine *agent.Engine
	tools, err := newSessionToolset(config, env, backendRuntime, metadata, settings, authorizeNetwork, func() error {
		if engine == nil {
			return errors.New("goal completion is unavailable")
		}
		return engine.CompleteGoal()
	})
	if err != nil {
		return nil, terminalOptionsBuilder{}, errors.Join(err, closeBackendRuntime(backendRuntime))
	}
	engine = agent.New(provider, tools.registry, engineOptions(
		config,
		config.models.main,
		settings,
		checkpointing,
		tools.subagents,
		subagentInstructions(tools.subagents),
	))
	session := &agentSession{
		engine:         engine,
		tools:          tools.registry,
		subagents:      tools.subagents,
		backendRuntime: backendRuntime,
		settings:       settings,
	}
	options := terminalOptionsBuilder{
		config:             config,
		runtime:            env,
		metadata:           metadata,
		warnings:           warnings,
		loadUsage:          loadUsage,
		subagentUpdates:    tools.subagentUpdates,
		permissionRequests: permissionRequests,
		engine:             engine,
	}
	return session, options, nil
}

func (session *agentSession) run(ctx context.Context, runner sessionRunner) (terminal.RunOutcome, error) {
	outcome, runErr := runner.Run(ctx, session.terminalOptions)
	return outcome, session.finish(runErr)
}

func (session *agentSession) finish(runErr error) error {
	if session.persistence != nil {
		session.persistence.stop()
	}
	var subagentErr error
	if session.subagents != nil {
		subagentErr = session.subagents.Close()
	}
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
	return errors.Join(runErr, subagentErr, finalCheckpointErr, persistenceErr, backendErr)
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
