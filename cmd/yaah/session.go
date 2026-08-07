package main

import (
	"context"
	"errors"
	"fmt"

	"yaah/agent"
	openaiadapter "yaah/provider/openai"
	"yaah/terminal"
	"yaah/tool"
)

type agentSession struct {
	engine          *agent.Engine
	tools           *tool.Registry
	terminalOptions terminal.Options
	prompt          string
	oneShot         bool
}

func newAgentSession(config agentConfig, runtime appRuntime, tokenSource openaiadapter.CodexTokenSource) (*agentSession, error) {
	provider, err := runtime.newProvider(tokenSource)
	if err != nil {
		return nil, fmt.Errorf("configure provider: %w", err)
	}
	var loadUsage func(context.Context) (agent.ProviderUsage, error)
	if usageProvider, ok := provider.(agent.UsageProvider); ok {
		loadUsage = usageProvider.Usage
	}

	currentThinkingLevel := config.thinkingLevel
	subagent := tool.NewSubagent(func(ctx context.Context, task string, usage func(agent.Usage)) (agent.RunResult, error) {
		return runChildAgent(ctx, runtime.newProvider, tokenSource, config, currentThinkingLevel, task, usage)
	})
	registry := buildTools(config.cwd, subagent)
	engine := agent.New(provider, registry, agent.Options{
		Model:               config.model,
		ThinkingLevel:       currentThinkingLevel,
		ProjectInstructions: config.projectInstructions,
	})
	setThinkingLevel := func(level agent.ThinkingLevel) error {
		if err := engine.SetThinkingLevel(level); err != nil {
			return err
		}
		currentThinkingLevel = level
		return nil
	}

	return &agentSession{
		engine:  engine,
		tools:   registry,
		prompt:  config.prompt,
		oneShot: config.oneShot,
		terminalOptions: terminal.Options{
			Input:            runtime.stdin,
			Output:           runtime.stdout,
			ErrorOutput:      runtime.stderr,
			Model:            config.model,
			WorkingDirectory: config.cwd,
			ThinkingLevel:    currentThinkingLevel,
			ThinkingLevels:   openaiadapter.SupportedThinkingLevels(config.model),
			ContextWindow:    openaiadapter.ContextWindow(config.model),
			Interrupts:       runtime.interrupts,
			SetThinkingLevel: setThinkingLevel,
			LoadUsage:        loadUsage,
		},
	}, nil
}

func (session *agentSession) run(ctx context.Context) error {
	var runErr error
	if session.oneShot {
		runErr = terminal.RunOneShot(ctx, session.engine, session.prompt, session.terminalOptions)
	} else {
		runErr = terminal.Run(ctx, session.engine, session.terminalOptions)
	}

	return session.finish(runErr)
}

func (session *agentSession) finish(runErr error) error {
	return finishRegistry(runErr, session.tools, "close tools")
}

func runChildAgent(
	ctx context.Context,
	newProvider providerFactory,
	tokenSource openaiadapter.CodexTokenSource,
	config agentConfig,
	thinkingLevel agent.ThinkingLevel,
	task string,
	usage func(agent.Usage),
) (agent.RunResult, error) {
	provider, err := newProvider(tokenSource)
	if err != nil {
		return agent.RunResult{}, fmt.Errorf("configure subagent provider: %w", err)
	}

	registry := buildSubagentTools(config.cwd)
	child := agent.New(provider, registry, agent.Options{
		Model:               config.model,
		ThinkingLevel:       thinkingLevel,
		ProjectInstructions: config.projectInstructions,
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

func buildTools(cwd string, additional ...tool.Tool) *tool.Registry {
	tools := []tool.Tool{
		tool.NewRead(cwd),
		tool.NewWrite(cwd),
		tool.NewEdit(cwd),
		tool.NewBash(cwd),
	}
	tools = append(tools, tool.NewLSP(cwd)...)
	tools = append(tools, additional...)
	return tool.NewRegistry(tools...)
}

func buildSubagentTools(cwd string) *tool.Registry {
	tools := []tool.Tool{tool.NewRead(cwd)}
	tools = append(tools, tool.NewReadOnlyLSP(cwd)...)
	return tool.NewRegistry(tools...)
}
