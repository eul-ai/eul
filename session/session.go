package session

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
	"github.com/eul-ai/eul/tool/subagent"
)

type sessionRunner interface {
	Run(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error)
}

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	subagents       *subagent.Manager
	backendRuntime  backend.Runtime
	terminalOptions terminal.Options
	thinkingLevel   agent.ThinkingLevel
	fastMode        bool
	persistence     *sessionHandle
	checkpoints     *checkpointCoordinator
	checkpointDone  chan struct{}
}

type sessionModelMetadata struct {
	main          agent.ModelMetadata
	subagent      map[subagent.Profile]agent.ModelMetadata
	thinkingLevel agent.ThinkingLevel
}

type sessionToolset struct {
	registry        *tool.Registry
	subagents       *subagent.Manager
	subagentUpdates <-chan terminal.SubagentStatus
}

func forwardSubagentStatus(source <-chan subagent.Status) <-chan terminal.SubagentStatus {
	updates := make(chan terminal.SubagentStatus, 1)
	go func() {
		defer close(updates)
		for status := range source {
			mapped := terminal.SubagentStatus{
				Running:    status.Running,
				Finalizing: status.Finalizing,
				Active:     make([]terminal.SubagentJobStatus, len(status.Active)),
				Awaiting:   make([]terminal.SubagentCompletionStatus, len(status.Awaiting)),
			}
			for index, job := range status.Active {
				mapped.Active[index] = terminal.SubagentJobStatus{
					ID:              job.ID,
					Task:            job.Task,
					ModelProfile:    string(job.ModelProfile),
					ThinkingLevel:   job.ThinkingLevel,
					State:           terminal.SubagentState(job.State),
					Started:         job.Started,
					Usage:           job.Usage,
					Generations:     job.Generations,
					GenerationLimit: job.GenerationLimit,
				}
			}
			for index, completion := range status.Awaiting {
				mapped.Awaiting[index] = terminal.SubagentCompletionStatus{
					MessageID:  completion.MessageID,
					SubagentID: completion.SubagentID,
					Task:       completion.Task,
					State:      terminal.SubagentState(completion.Status),
					Started:    completion.Started,
					Finished:   completion.Finished,
				}
			}
			select {
			case updates <- mapped:
			default:
				select {
				case <-updates:
				default:
				}
				updates <- mapped
			}
		}
	}()
	return updates
}

type terminalOptionSource struct {
	config             resolvedConfig
	runtime            runtime
	metadata           sessionModelMetadata
	warnings           []string
	loadUsage          func(context.Context) (agent.ProviderUsage, error)
	subagentUpdates    <-chan terminal.SubagentStatus
	permissionRequests <-chan terminal.PermissionRequest
	setThinkingLevel   func(agent.ThinkingLevel) error
	setFastMode        func(bool) error
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
		subagent: map[subagent.Profile]agent.ModelMetadata{
			subagent.ProfileFast:     resolve(config.models.subagent(subagent.ProfileFast)),
			subagent.ProfileBalanced: resolve(config.models.subagent(subagent.ProfileBalanced)),
			subagent.ProfilePowerful: resolve(config.models.subagent(subagent.ProfilePowerful)),
		},
		thinkingLevel: main.ClampThinkingLevel(config.thinkingLevel),
	}
}

func newSessionToolset(
	config resolvedConfig,
	runtime runtime,
	backendRuntime backend.Runtime,
	metadata sessionModelMetadata,
	fastMode *atomic.Bool,
	authorizeNetwork tool.NetworkAuthorizer,
	completeGoal func() error,
) (sessionToolset, error) {
	newToolset := runtime.newToolset
	if newToolset == nil {
		newToolset = func(cwd string, access toolAccess, noSandbox bool, authorizeNetwork tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
			return buildToolsetWithHomeAndNetworkAuthorizer(cwd, "", access, noSandbox, authorizeNetwork, additional...)
		}
	}
	manager := subagent.NewManager(func(ctx context.Context, task string, profile subagent.Profile, thinkingLevel agent.ThinkingLevel, update func(subagent.Progress)) (agent.RunResult, error) {
		enabled := fastMode.Load() && metadata.subagent[profile].FastMode
		return runChildAgent(ctx, backendRuntime, newToolset, config, profile, thinkingLevel, enabled, task, update)
	}, func(profile subagent.Profile) []agent.ThinkingLevel {
		return metadata.subagent[profile].ThinkingLevels
	})
	registry, err := newToolset(
		config.cwd,
		fullToolAccess,
		config.noSandbox,
		authorizeNetwork,
		subagent.NewLaunchTool(manager),
		subagent.NewWaitTool(manager),
		subagent.NewCancelTool(manager),
		tool.NewUpdateGoal(completeGoal),
	)
	if err != nil {
		return sessionToolset{}, errors.Join(fmt.Errorf("configure tools: %w", err), manager.Close())
	}
	updates := forwardSubagentStatus(manager.StatusUpdates())
	return sessionToolset{registry: registry, subagents: manager, subagentUpdates: updates}, nil
}

func (source terminalOptionSource) options() terminal.Options {
	return terminal.Options{
		Input:              source.runtime.stdin,
		Output:             source.runtime.stdout,
		Model:              source.config.models.main,
		WorkingDirectory:   source.config.cwd,
		ThinkingLevel:      source.metadata.thinkingLevel,
		ThinkingLevels:     source.metadata.main.ThinkingLevels,
		FastMode:           source.config.fastMode,
		FastModeAvailable:  source.metadata.main.FastMode,
		ContextWindow:      source.metadata.main.ContextWindow,
		Skills:             source.config.skills,
		Warnings:           source.warnings,
		Interrupts:         source.runtime.interrupts,
		SetThinkingLevel:   source.setThinkingLevel,
		SetFastMode:        source.setFastMode,
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
	metadata := resolveSessionModelMetadata(provider, config)
	if config.fastMode && !metadata.main.FastMode {
		return nil, errors.Join(fmt.Errorf("fast mode is unavailable for model %q", config.models.main), closeBackendRuntime(backendRuntime))
	}
	loadUsage, usageWarning := dedicatedUsageLoader(backendRuntime, provider)
	warnings := append([]string(nil), config.warnings...)
	if usageWarning != "" {
		warnings = append(warnings, usageWarning)
	}
	authorizeNetwork, permissionRequests := newNetworkPermissionBroker(config.noSandbox)
	var fastMode atomic.Bool
	fastMode.Store(config.fastMode)

	var engine *agent.Engine
	tools, err := newSessionToolset(config, runtime, backendRuntime, metadata, &fastMode, authorizeNetwork, func() error {
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
		FastMode:            config.fastMode,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
		Checkpointing:       checkpointing,
		Inbox:               tools.subagents,
	})
	session := &agentSession{
		engine:         engine,
		tools:          tools.registry,
		subagents:      tools.subagents,
		backendRuntime: backendRuntime,
		thinkingLevel:  metadata.thinkingLevel,
		fastMode:       fastMode.Load(),
	}
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		if err := engine.SetThinkingLevel(level); err != nil {
			return err
		}
		session.thinkingLevel = level
		if session.checkpoints != nil {
			session.checkpoints.SetThinkingLevel(level)
		}
		return nil
	}
	setFastMode := func(enabled bool) error {
		engine.SetFastMode(enabled)
		fastMode.Store(enabled)
		session.fastMode = enabled
		if session.checkpoints != nil {
			session.checkpoints.SetFastMode(enabled)
		}
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
		setFastMode:        setFastMode,
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
			session.subagents.Checkpoint(),
			terminal.EmptyCheckpoint(),
			session.fastMode,
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
	session.checkpoints = newCheckpointCoordinator(
		handle,
		session.engine,
		session.subagents,
		session.thinkingLevel,
		session.fastMode,
	)
	record := handle.Record()
	session.terminalOptions.SessionID = record.ID
	if restore {
		if err := session.engine.RestoreCheckpoint(record.Agent); err != nil {
			return fmt.Errorf("restore agent session: %w", err)
		}
		if err := session.subagents.RestoreCheckpoint(record.Subagent); err != nil {
			return fmt.Errorf("restore subagents: %w", err)
		}
		checkpoint := record.Terminal
		session.terminalOptions.InitialCheckpoint = &checkpoint
		session.terminalOptions.PreviousTurnActive = record.Status == sessionActive
		if err := session.checkpoints.RestoreIdle(record.Agent); err != nil {
			return fmt.Errorf("save restored subagents: %w", err)
		}
	}

	session.terminalOptions.SaveCheckpoint = session.checkpoints.SaveTerminal
	session.checkpointDone = make(chan struct{})
	go func() {
		defer close(session.checkpointDone)
		for range session.subagents.CheckpointUpdates() {
			session.checkpoints.SaveIdle()
		}
	}()

	session.terminalOptions.ListSessions = func(context.Context) ([]terminal.SessionSummary, []string, error) {
		summaries, warnings, err := handle.store.List(record.WorkingDirectory)
		if err != nil {
			return nil, nil, err
		}
		visible := summaries[:0]
		for _, summary := range summaries {
			if summary.ID != record.ID {
				visible = append(visible, summary)
			}
		}
		return visible, warnings, nil
	}
	return nil
}

func (session *agentSession) run(ctx context.Context, runner sessionRunner) (terminal.RunOutcome, error) {
	outcome, runErr := runner.Run(ctx, session.engine, session.terminalOptions)
	cleanupErr := session.finish(nil)
	if cleanupErr != nil {
		return terminal.RunOutcome{}, cleanupErr
	}
	return outcome, runErr
}

func (session *agentSession) finish(runErr error) error {
	if session.checkpoints != nil {
		session.checkpoints.Stop()
	}
	var subagentErr error
	if session.subagents != nil {
		subagentErr = session.subagents.Close()
	}
	if session.checkpointDone != nil {
		<-session.checkpointDone
	}
	var finalCheckpointErr error
	if session.checkpoints != nil {
		finalCheckpointErr = session.checkpoints.SaveFinal()
	}
	toolErr := finishRegistry(nil, session.tools, "close tools")
	var persistenceErr error
	if session.checkpoints != nil {
		persistenceErr = session.checkpoints.Close()
	} else if session.persistence != nil {
		persistenceErr = session.persistence.Close()
	}
	if persistenceErr != nil {
		persistenceErr = fmt.Errorf("close session: %w", persistenceErr)
	}
	backendErr := closeBackendRuntime(session.backendRuntime)
	session.backendRuntime = nil
	return errors.Join(runErr, subagentErr, finalCheckpointErr, toolErr, persistenceErr, backendErr)
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
