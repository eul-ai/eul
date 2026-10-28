package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

type agentSession struct {
	config          sessionSettings
	engine          *agent.Engine
	subagents       *subagent.Manager
	terminalOptions terminal.Options
	agentSettings   *agent.Settings
	conversation    *session.Session
}

func newStoredAgentSession(
	launcher *launcher,
	config resolvedConfig,
	runtime *backend.Runtime,
	handle *session.Handle,
	prompt string,
) (*agentSession, error) {
	record := handle.Record()
	historyEntries, err := launcher.history.Load(record.ID)
	if err != nil {
		return nil, fmt.Errorf("load message history: %w", err)
	}
	stored, options, err := newAgentSessionComponents(config, runtime, record, launcher.noSandbox, launcher.interrupts)
	if err != nil {
		return nil, err
	}

	stored.conversation = session.New(handle, stored.engine, stored.subagents, stored.agentSettings, config.warnings)
	options.conversation = stored.conversation
	options.messageHistory = terminal.MessageHistory{
		Entries: historyEntries,
		Append: func(entry string) error {
			return launcher.history.Append(record.ID, entry)
		},
	}
	options.draft = record.Draft
	options.contextTokens = record.Agent.ContextUsage.TotalTokens
	options.initialPrompt = prompt
	options.sessionID = record.ID
	options.sessions.List = func(context.Context) ([]session.Summary, error) {
		return launcher.store.List(record.WorkingDirectory)
	}
	stored.terminalOptions = options.options()
	return stored, nil
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
	runtime *backend.Runtime,
	record session.Record,
	noSandbox bool,
	interrupts <-chan os.Signal,
) (*agentSession, terminalOptionsBuilder, error) {
	metadata, err := resolveModelMetadata(runtime, config.models)
	if err != nil {
		return nil, terminalOptionsBuilder{}, err
	}
	main := metadata[subagent.ProfileMain]
	if config.fastMode && !main.FastMode {
		return nil, terminalOptionsBuilder{}, fmt.Errorf("fast mode is unavailable for model %q", config.models.Main)
	}
	authorizeNetwork, permissionRequests := newNetworkPermissionBroker(noSandbox)
	settings := agent.NewSettings(main.ClampThinkingLevel(config.thinkingLevel), config.fastMode)

	goal := &agent.Goal{}
	tools, subagents := newSessionTools(config, runtime.Provider, metadata, settings, authorizeNetwork, goal, record.Subagent)
	engine := agent.New(runtime.Provider, tools, agent.Options{
		Model:         config.models.Main,
		Instructions:  systemPrompt(config),
		SessionID:     record.ID,
		Checkpoint:    record.Agent,
		Checkpointing: true,
		Inbox:         tool.NewSubagentInbox(subagents),
		Settings:      settings,
		Goal:          goal,
	})
	session := &agentSession{
		config:        config.sessionSettings,
		engine:        engine,
		subagents:     subagents,
		agentSettings: settings,
	}
	terminalOptions := terminalOptionsBuilder{
		config:             config,
		interrupts:         interrupts,
		metadata:           main,
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

func (session *agentSession) run(ctx context.Context, run func(context.Context, terminal.Options) (terminal.RunOutcome, error)) (terminal.RunOutcome, error) {
	outcome, runErr := run(ctx, session.terminalOptions)
	return outcome, session.finish(runErr)
}

func (session *agentSession) finish(runErr error) error {
	session.subagents.Close()
	return errors.Join(runErr, session.conversation.Close())
}
