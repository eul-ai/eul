package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type sessionRunner interface {
	Run(context.Context, terminal.Engine, terminal.Options) error
}

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	backendRuntime  backend.Runtime
	terminalOptions terminal.Options
	thinkingLevel   agent.ThinkingLevel
	persistence     *sessionHandle
}

type sessionModelMetadata struct {
	main          agent.ModelMetadata
	subagent      map[tool.SubagentModelProfile]agent.ModelMetadata
	thinkingLevel agent.ThinkingLevel
}

type sessionToolset struct {
	registry        *tool.Registry
	subagentUpdates <-chan agent.SubagentStatus
}

type terminalOptionSource struct {
	config             resolvedConfig
	runtime            runtime
	metadata           sessionModelMetadata
	warnings           []string
	loadUsage          func(context.Context) (agent.ProviderUsage, error)
	subagentUpdates    <-chan agent.SubagentStatus
	permissionRequests <-chan terminal.PermissionRequest
	setThinkingLevel   func(agent.ThinkingLevel) error
}

func providerModelMetadata(provider agent.Provider, model string) agent.ModelMetadata {
	metadataProvider, ok := provider.(agent.ModelMetadataProvider)
	if !ok {
		return agent.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}
	metadata := metadataProvider.ModelMetadata(model)
	if len(metadata.ThinkingLevels) == 0 {
		metadata.ThinkingLevels = []agent.ThinkingLevel{agent.ThinkingOff}
	}
	return metadata
}

func resolveSessionModelMetadata(provider agent.Provider, config resolvedConfig) sessionModelMetadata {
	metadataByModel := make(map[string]agent.ModelMetadata)
	resolve := func(model string) agent.ModelMetadata {
		metadata, ok := metadataByModel[model]
		if !ok {
			metadata = providerModelMetadata(provider, model)
			metadataByModel[model] = metadata
		}
		return metadata
	}

	main := resolve(config.models.main)
	return sessionModelMetadata{
		main: main,
		subagent: map[tool.SubagentModelProfile]agent.ModelMetadata{
			tool.SubagentModelFast:     resolve(config.models.subagent(tool.SubagentModelFast)),
			tool.SubagentModelBalanced: resolve(config.models.subagent(tool.SubagentModelBalanced)),
			tool.SubagentModelPowerful: resolve(config.models.subagent(tool.SubagentModelPowerful)),
		},
		thinkingLevel: main.ClampThinkingLevel(config.thinkingLevel),
	}
}

func newSessionToolset(
	config resolvedConfig,
	runtime runtime,
	backendRuntime backend.Runtime,
	metadata sessionModelMetadata,
	authorizeNetwork tool.NetworkAuthorizer,
	completeGoal func() error,
) (sessionToolset, error) {
	newToolset := runtime.newToolset
	if newToolset == nil {
		newToolset = func(cwd string, access toolAccess, authorizeNetwork tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
			return buildToolsetWithHomeAndNetworkAuthorizer(cwd, "", access, authorizeNetwork, additional...)
		}
	}
	subagent := tool.NewSubagentWithThinkingLevels(func(ctx context.Context, task string, modelProfile tool.SubagentModelProfile, thinkingLevel agent.ThinkingLevel, update func(tool.SubagentProgress)) (agent.RunResult, error) {
		return runChildAgent(ctx, backendRuntime, newToolset, config, modelProfile, thinkingLevel, task, update)
	}, func(profile tool.SubagentModelProfile) []agent.ThinkingLevel {
		return metadata.subagent[profile].ThinkingLevels
	})
	registry, err := newToolset(
		config.cwd,
		fullToolAccess,
		authorizeNetwork,
		subagent,
		tool.NewSubagentWait(subagent),
		tool.NewSubagentCancel(subagent),
		tool.NewUpdateGoal(completeGoal),
	)
	if err != nil {
		return sessionToolset{}, errors.Join(fmt.Errorf("configure tools: %w", err), subagent.Close())
	}
	return sessionToolset{registry: registry, subagentUpdates: subagent.StatusUpdates()}, nil
}

func (source terminalOptionSource) options() terminal.Options {
	return terminal.Options{
		Input:              source.runtime.stdin,
		Output:             source.runtime.stdout,
		Model:              source.config.models.main,
		WorkingDirectory:   source.config.cwd,
		ThinkingLevel:      source.metadata.thinkingLevel,
		ThinkingLevels:     source.metadata.main.ThinkingLevels,
		ContextWindow:      source.metadata.main.ContextWindow,
		Skills:             source.config.skills,
		Warnings:           source.warnings,
		Interrupts:         source.runtime.interrupts,
		SetThinkingLevel:   source.setThinkingLevel,
		LoadUsage:          source.loadUsage,
		SubagentUpdates:    source.subagentUpdates,
		PermissionRequests: source.permissionRequests,
	}
}

func newAgentSession(config resolvedConfig, runtime runtime, backendRuntime backend.Runtime) (*agentSession, error) {
	return newAgentSessionWithCheckpointing(config, runtime, backendRuntime, false)
}

func newAgentSessionWithCheckpointing(
	config resolvedConfig,
	runtime runtime,
	backendRuntime backend.Runtime,
	checkpointing bool,
) (*agentSession, error) {
	provider, err := backendRuntime.NewProvider()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("configure provider: %w", err), closeBackendRuntime(backendRuntime))
	}
	loadUsage, usageWarning := dedicatedUsageLoader(backendRuntime, provider)
	warnings := append([]string(nil), config.warnings...)
	if usageWarning != "" {
		warnings = append(warnings, usageWarning)
	}
	metadata := resolveSessionModelMetadata(provider, config)
	authorizeNetwork, permissionRequests := newNetworkPermissionBroker(config.skipPermissions)

	var engine *agent.Engine
	tools, err := newSessionToolset(config, runtime, backendRuntime, metadata, authorizeNetwork, func() error {
		if engine == nil {
			return errors.New("goal completion is unavailable")
		}
		return engine.CompleteGoal()
	})
	if err != nil {
		return nil, errors.Join(err, closeBackendRuntime(backendRuntime))
	}
	engine = agent.New(provider, tools.registry, agent.Options{
		Model:               config.models.main,
		ThinkingLevel:       metadata.thinkingLevel,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
		Checkpointing:       checkpointing,
	})
	session := &agentSession{
		engine:         engine,
		tools:          tools.registry,
		backendRuntime: backendRuntime,
		thinkingLevel:  metadata.thinkingLevel,
	}
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		if err := engine.SetThinkingLevel(level); err != nil {
			return err
		}
		session.thinkingLevel = level
		return nil
	}
	session.terminalOptions = terminalOptionSource{
		config:             config,
		runtime:            runtime,
		metadata:           metadata,
		warnings:           warnings,
		loadUsage:          loadUsage,
		subagentUpdates:    tools.subagentUpdates,
		permissionRequests: permissionRequests,
		setThinkingLevel:   setThinkingLevel,
	}.options()
	return session, nil
}

func dedicatedUsageLoader(backendRuntime backend.Runtime, provider agent.Provider) (func(context.Context) (agent.ProviderUsage, error), string) {
	if _, ok := provider.(agent.UsageProvider); !ok {
		return nil, ""
	}

	dedicated, err := backendRuntime.NewProvider()
	if err != nil {
		return nil, fmt.Sprintf("Account usage is unavailable: %v", err)
	}
	usageProvider, ok := dedicated.(agent.UsageProvider)
	if !ok {
		return nil, "Account usage is unavailable: dedicated provider does not support usage"
	}
	return usageProvider.Usage, ""
}

func newStoredAgentSession(
	config resolvedConfig,
	runtime runtime,
	backendRuntime backend.Runtime,
	store *sessionStore,
	handle *sessionHandle,
) (*agentSession, error) {
	session, err := newAgentSessionWithCheckpointing(config, runtime, backendRuntime, true)
	if err != nil {
		if handle != nil {
			_ = handle.Close()
		}
		return nil, err
	}

	restore := handle != nil
	if handle == nil {
		agentCheckpoint, checkpointErr := session.engine.Checkpoint()
		if checkpointErr != nil {
			return nil, session.finish(checkpointErr)
		}
		handle, err = store.Create(
			config.provider,
			config.cwd,
			config.models,
			session.thinkingLevel,
			agentCheckpoint,
			terminal.EmptyCheckpoint(),
		)
		if err != nil {
			return nil, session.finish(err)
		}
	}
	if err := session.attachPersistence(handle, restore); err != nil {
		return nil, session.finish(err)
	}
	return session, nil
}

func (session *agentSession) attachPersistence(handle *sessionHandle, restore bool) error {
	session.persistence = handle
	session.terminalOptions.SessionID = handle.record.ID
	if restore {
		if err := session.engine.RestoreCheckpoint(handle.record.Agent); err != nil {
			return fmt.Errorf("restore agent session: %w", err)
		}
		checkpoint := handle.record.Terminal
		session.terminalOptions.InitialCheckpoint = &checkpoint
		session.terminalOptions.PreviousTurnActive = handle.record.Status == sessionActive
	}

	session.terminalOptions.SaveCheckpoint = func(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint, active bool) error {
		return handle.Save(agentCheckpoint, terminalCheckpoint, active, session.thinkingLevel)
	}
	session.terminalOptions.ListSessions = func(context.Context) ([]terminal.SessionSummary, []string, error) {
		summaries, warnings, err := handle.store.List(handle.record.WorkingDirectory)
		if err != nil {
			return nil, nil, err
		}
		visible := summaries[:0]
		for _, summary := range summaries {
			if summary.ID != handle.record.ID {
				visible = append(visible, summary)
			}
		}
		return visible, warnings, nil
	}
	return nil
}

func (session *agentSession) run(ctx context.Context, runner sessionRunner) error {
	return session.finish(runner.Run(ctx, session.engine, session.terminalOptions))
}

func (session *agentSession) finish(runErr error) error {
	toolErr := finishRegistry(nil, session.tools, "close tools")
	var persistenceErr error
	if session.persistence != nil {
		persistenceErr = session.persistence.Close()
		if persistenceErr != nil {
			persistenceErr = fmt.Errorf("close session: %w", persistenceErr)
		}
	}
	backendErr := closeBackendRuntime(session.backendRuntime)
	session.backendRuntime = nil
	return errors.Join(runErr, toolErr, persistenceErr, backendErr)
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
