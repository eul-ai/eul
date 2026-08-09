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
}

func newAgentSession(
	config agentConfig,
	runtime appRuntime,
	tokenSource openaiadapter.CodexTokenSource,
	providerOptions openaiadapter.Options,
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
	subagent := tool.NewSubagent(func(ctx context.Context, task string, usage func(agent.Usage)) (agent.RunResult, error) {
		return runChildAgent(ctx, runtime.newProvider, newToolset, tokenSource, providerOptions, config, currentThinkingLevel, task, usage)
	})
	var engine *agent.Engine
	updateGoal := tool.NewUpdateGoal(func() error {
		if engine == nil {
			return errors.New("goal completion is unavailable")
		}
		return engine.CompleteGoal()
	})
	registry, err := newToolset(config.cwd, fullToolAccess, subagent, updateGoal)
	if err != nil {
		return nil, fmt.Errorf("configure tools: %w", err)
	}
	engine = agent.New(provider, registry, agent.Options{
		Model:               config.model,
		ThinkingLevel:       currentThinkingLevel,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
	})
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		if err := engine.SetThinkingLevel(level); err != nil {
			return err
		}
		currentThinkingLevel = level
		return nil
	}

	return &agentSession{
		engine: engine,
		tools:  registry,
		terminalOptions: terminal.Options{
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
		},
	}, nil
}

func (session *agentSession) run(ctx context.Context) error {
	return session.finish(terminal.Run(ctx, session.engine, session.terminalOptions))
}

func (session *agentSession) finish(runErr error) error {
	return finishRegistry(runErr, session.tools, "close tools")
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
	usage func(agent.Usage),
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
	result, runErr := child.Run(ctx, task, func(event agent.Event) error {
		switch event.Kind {
		case agent.EventCompactionEnd, agent.EventContextUsage:
			liveUsage.InputTokens += event.Usage.InputTokens
			liveUsage.OutputTokens += event.Usage.OutputTokens
			liveUsage.TotalTokens += event.Usage.TotalTokens
			usage(liveUsage)
		}
		return nil
	})
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
	var tools []tool.Tool
	var lsp *lsptool.Set
	switch access {
	case fullToolAccess:
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewEdit(cwd),
			tool.NewBash(cwd),
		}
		lsp = lsptool.New(cwd)
	case readOnlyToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd)}
		lsp = lsptool.NewReadOnly(cwd)
	default:
		return nil, errors.New("unknown tool access")
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
