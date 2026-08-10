package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
	openaiadapter "github.com/eul-ai/eul/provider/openai"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
	lsptool "github.com/eul-ai/eul/tool/lsp"
)

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	terminalOptions terminal.Options
	thinkingLevel   agent.ThinkingLevel
	persistence     *sessionHandle
}

func resolveInitialSession(
	ctx context.Context,
	arguments agentArguments,
	runtime appRuntime,
	store *sessionStore,
) (agentConfig, *sessionHandle, error) {
	if !arguments.resume {
		config, err := resolveAgentConfig(arguments, runtime)
		return config, nil, err
	}

	cwd, err := resolveCWD("", runtime.getwd)
	if err != nil {
		return agentConfig{}, nil, err
	}
	return resolveStoredSession(ctx, store, runtime, cwd, arguments.sessionID)
}

func resolveStoredSession(
	ctx context.Context,
	store *sessionStore,
	runtime appRuntime,
	lookupCWD string,
	sessionID string,
) (agentConfig, *sessionHandle, error) {
	handle, err := store.Open(ctx, lookupCWD, sessionID)
	if err != nil {
		return agentConfig{}, nil, err
	}
	record := handle.Record()
	config, err := resolveAgentConfig(agentArguments{
		model:         record.Model,
		thinkingLevel: record.ThinkingLevel,
		cwd:           record.WorkingDirectory,
	}, runtime)
	if err != nil {
		_ = handle.Close()
		return agentConfig{}, nil, err
	}
	return config, handle, nil
}

func newAgentSession(
	config agentConfig,
	runtime appRuntime,
	tokenSource openaiadapter.CodexTokenSource,
	providerOptions openaiadapter.Options,
) (*agentSession, error) {
	return newAgentSessionWithCheckpointing(config, runtime, tokenSource, providerOptions, false)
}

func newAgentSessionWithCheckpointing(
	config agentConfig,
	runtime appRuntime,
	tokenSource openaiadapter.CodexTokenSource,
	providerOptions openaiadapter.Options,
	checkpointing bool,
) (*agentSession, error) {
	provider, err := runtime.newProvider(tokenSource, providerOptions)
	if err != nil {
		return nil, fmt.Errorf("configure provider: %w", err)
	}
	var loadUsage func(context.Context) (agent.ProviderUsage, error)
	if usageProvider, ok := provider.(agent.UsageProvider); ok {
		loadUsage = usageProvider.Usage
	}

	metadata := agent.ModelMetadata{ThinkingLevels: agent.ThinkingLevels()}
	if metadataProvider, ok := provider.(agent.ModelMetadataProvider); ok {
		metadata = metadataProvider.ModelMetadata(config.model)
	}
	currentThinkingLevel := metadata.ClampThinkingLevel(config.thinkingLevel)
	newToolset := runtime.newToolset
	if newToolset == nil {
		newToolset = buildToolset
	}
	subagent := tool.NewSubagent(func(ctx context.Context, task string, thinkingLevel agent.ThinkingLevel, update func(tool.SubagentProgress)) (agent.RunResult, error) {
		return runChildAgent(ctx, runtime.newProvider, newToolset, tokenSource, providerOptions, config, thinkingLevel, task, update)
	}, metadata.ThinkingLevels...)
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
		_ = subagent.Close()
		return nil, fmt.Errorf("configure tools: %w", err)
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
		engine:        engine,
		tools:         registry,
		thinkingLevel: currentThinkingLevel,
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
		Interrupts:       runtime.interrupts,
		SetThinkingLevel: setThinkingLevel,
		LoadUsage:        loadUsage,
		SubagentUpdates:  subagent.StatusUpdates(),
	}
	return session, nil
}

func newStoredAgentSession(
	config agentConfig,
	runtime appRuntime,
	tokenSource openaiadapter.CodexTokenSource,
	providerOptions openaiadapter.Options,
	store *sessionStore,
	handle *sessionHandle,
) (*agentSession, error) {
	session, err := newAgentSessionWithCheckpointing(config, runtime, tokenSource, providerOptions, true)
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
		handle, err = store.Create(config.cwd, config.model, session.thinkingLevel, agentCheckpoint, terminal.EmptyCheckpoint())
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
	session.terminalOptions.ListSessions = func(context.Context) ([]terminal.SessionSummary, error) {
		summaries, err := handle.store.List(handle.record.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		visible := summaries[:0]
		for _, summary := range summaries {
			if summary.ID != handle.record.ID {
				visible = append(visible, summary)
			}
		}
		return visible, nil
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
	return errors.Join(runErr, toolErr, persistenceErr)
}

func runChildAgent(
	ctx context.Context,
	newProvider providerFactory,
	newToolset toolsetFactory,
	tokenSource openaiadapter.CodexTokenSource,
	providerOptions openaiadapter.Options,
	config agentConfig,
	thinkingLevel agent.ThinkingLevel,
	task string,
	update func(tool.SubagentProgress),
) (agent.RunResult, error) {
	provider, err := newProvider(tokenSource, providerOptions)
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent provider: %w", err)
	}

	registry, err := newToolset(config.cwd, readOnlyToolAccess)
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent tools: %w", err)
	}
	child := agent.New(provider, registry, agent.Options{
		Model:               config.model,
		ThinkingLevel:       thinkingLevel,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
	})
	var liveUsage agent.Usage
	policy := tool.NewSubagentFinalizationPolicy(func() {
		update(tool.SubagentProgress{Usage: liveUsage, Finalizing: true})
	})
	result, runErr := child.RunWithFinalization(ctx, task, func(event agent.Event) error {
		switch event.Kind {
		case agent.EventCompactionEnd, agent.EventContextUsage:
			liveUsage.InputTokens += event.Usage.InputTokens
			liveUsage.OutputTokens += event.Usage.OutputTokens
			liveUsage.TotalTokens += event.Usage.TotalTokens
			update(tool.SubagentProgress{Usage: liveUsage})
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
