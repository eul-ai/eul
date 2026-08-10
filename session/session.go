package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
	lsptool "github.com/eul-ai/eul/tool/lsp"
)

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	backendRuntime  backend.Runtime
	terminalOptions terminal.Options
	thinkingLevel   agent.ThinkingLevel
	persistence     *sessionHandle
}

func resolveInitialSession(
	ctx context.Context,
	arguments Options,
	runtime runtime,
	store *sessionStore,
) (config, *sessionHandle, backend.Driver, error) {
	if !arguments.Resume {
		driver, err := runtime.backends.Lookup(arguments.Provider)
		if err != nil {
			return config{}, nil, nil, err
		}
		config, err := resolveConfig(arguments, runtime, driver.Descriptor(), driver.ModelDefaults())
		return config, nil, driver, err
	}

	cwd, err := resolveCWD("", runtime.getwd)
	if err != nil {
		return config{}, nil, nil, err
	}
	return resolveStoredSession(ctx, store, runtime, cwd, arguments.SessionID)
}

func resolveStoredSession(
	ctx context.Context,
	store *sessionStore,
	runtime runtime,
	lookupCWD string,
	sessionID string,
) (config, *sessionHandle, backend.Driver, error) {
	handle, err := store.Open(ctx, lookupCWD, sessionID)
	if err != nil {
		return config{}, nil, nil, err
	}
	record := handle.Record()
	driver, err := runtime.backends.Lookup(record.Provider)
	if err != nil {
		_ = handle.Close()
		return config{}, nil, nil, err
	}
	resolved, err := resolveConfig(Options{
		Model:            record.Model,
		ModelSet:         true,
		FastModel:        record.FastModel,
		FastModelSet:     record.FastModel != "",
		BalancedModel:    record.BalancedModel,
		BalancedModelSet: record.BalancedModel != "",
		ThinkingLevel:    record.ThinkingLevel,
		WorkingDirectory: record.WorkingDirectory,
	}, runtime, driver.Descriptor(), driver.ModelDefaults())
	if err != nil {
		_ = handle.Close()
		return config{}, nil, nil, err
	}
	resolved.warnings = append(resolved.warnings, handle.warnings...)
	return resolved, handle, driver, nil
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

func newAgentSession(config config, runtime runtime, backendRuntime backend.Runtime) (*agentSession, error) {
	return newAgentSessionWithCheckpointing(config, runtime, backendRuntime, false)
}

func newAgentSessionWithCheckpointing(
	config config,
	runtime runtime,
	backendRuntime backend.Runtime,
	checkpointing bool,
) (*agentSession, error) {
	provider, err := backendRuntime.NewProvider()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("configure provider: %w", err), closeBackendRuntime(backendRuntime))
	}
	var loadUsage func(context.Context) (agent.ProviderUsage, error)
	if usageProvider, ok := provider.(agent.UsageProvider); ok {
		loadUsage = usageProvider.Usage
	}

	metadataByModel := make(map[string]agent.ModelMetadata)
	resolveMetadata := func(model string) agent.ModelMetadata {
		metadata, ok := metadataByModel[model]
		if !ok {
			metadata = providerModelMetadata(provider, model)
			metadataByModel[model] = metadata
		}
		return metadata
	}
	metadata := resolveMetadata(config.model)
	currentThinkingLevel := metadata.ClampThinkingLevel(config.thinkingLevel)
	subagentMetadata := map[tool.SubagentModelProfile]agent.ModelMetadata{
		tool.SubagentModelFast:     resolveMetadata(config.subagentModel(tool.SubagentModelFast)),
		tool.SubagentModelBalanced: resolveMetadata(config.subagentModel(tool.SubagentModelBalanced)),
		tool.SubagentModelPowerful: resolveMetadata(config.subagentModel(tool.SubagentModelPowerful)),
	}
	newToolset := runtime.newToolset
	if newToolset == nil {
		newToolset = buildToolset
	}
	subagent := tool.NewSubagentWithThinkingLevels(func(ctx context.Context, task string, modelProfile tool.SubagentModelProfile, thinkingLevel agent.ThinkingLevel, update func(tool.SubagentProgress)) (agent.RunResult, error) {
		return runChildAgent(ctx, backendRuntime, newToolset, config, modelProfile, thinkingLevel, task, update)
	}, func(profile tool.SubagentModelProfile) []agent.ThinkingLevel {
		return subagentMetadata[profile].ThinkingLevels
	})
	subagentWait := tool.NewSubagentWait(subagent)
	subagentCancel := tool.NewSubagentCancel(subagent)
	var engine *agent.Engine
	updateGoal := tool.NewUpdateGoal(func() error {
		if engine == nil {
			return errors.New("goal completion is unavailable")
		}
		return engine.CompleteGoal()
	})
	registry, err := newToolset(config.cwd, fullToolAccess, subagent, subagentWait, subagentCancel, updateGoal)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("configure tools: %w", err),
			subagent.Close(),
			closeBackendRuntime(backendRuntime),
		)
	}
	engine = agent.New(provider, registry, agent.Options{
		Model:               config.model,
		ThinkingLevel:       currentThinkingLevel,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
		Checkpointing:       checkpointing,
	})
	session := &agentSession{
		engine:         engine,
		tools:          registry,
		backendRuntime: backendRuntime,
		thinkingLevel:  currentThinkingLevel,
	}
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		if err := engine.SetThinkingLevel(level); err != nil {
			return err
		}
		session.thinkingLevel = level
		return nil
	}

	session.terminalOptions = terminal.Options{
		Input:            runtime.stdin,
		Output:           runtime.stdout,
		Model:            config.model,
		WorkingDirectory: config.cwd,
		ThinkingLevel:    currentThinkingLevel,
		ThinkingLevels:   metadata.ThinkingLevels,
		ContextWindow:    metadata.ContextWindow,
		Skills:           config.skills,
		Warnings:         config.warnings,
		Interrupts:       runtime.interrupts,
		SetThinkingLevel: setThinkingLevel,
		LoadUsage:        loadUsage,
		SubagentUpdates:  subagent.StatusUpdates(),
	}
	return session, nil
}

func newStoredAgentSession(
	config config,
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
			config.model,
			config.subagentFastModel,
			config.subagentBalancedModel,
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

func (session *agentSession) run(ctx context.Context, runner *terminal.Runner) error {
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

func finishRegistry(runErr error, registry *tool.Registry, operation string) error {
	closeErr := registry.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("%s: %w", operation, closeErr)
	}
	return errors.Join(runErr, closeErr)
}

func buildToolset(cwd string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	return buildToolsetWithHome(cwd, "", access, additional...)
}

func buildToolsetWithHome(cwd, home string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	var tools []tool.Tool
	var lsp *lsptool.Set
	var err error
	switch access {
	case fullToolAccess:
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewEdit(cwd),
			tool.NewBash(cwd),
		}
		lsp, err = lsptool.New(cwd, home)
	case readOnlyToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd)}
		lsp, err = lsptool.NewReadOnly(cwd, home)
	default:
		return nil, errors.New("unknown tool access")
	}
	if err != nil {
		return nil, fmt.Errorf("configure LSP: %w", err)
	}

	tools = append(tools, lsp.Tools()...)
	tools = append(tools, additional...)
	registry, err := tool.NewRegistry(tools, lsp)
	if err != nil {
		_ = lsp.Close()
		return nil, err
	}
	return registry, nil
}
