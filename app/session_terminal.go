package app

import (
	"context"
	"slices"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type terminalOptionsBuilder struct {
	config             resolvedConfig
	runtime            environment
	metadata           backend.ModelMetadata
	warnings           []string
	loadUsage          func(context.Context) (backend.AccountUsage, error)
	subagents          *subagent.Manager
	permissionRequests <-chan terminal.PermissionRequest
	engine             *agent.Engine
	settings           *agent.Settings
	goal               *agent.Goal
	checkpoints        *checkpointCoordinator
	sessions           terminal.Sessions
	messageHistory     terminal.MessageHistory
	initialCheckpoint  *terminal.Checkpoint
	initialPrompt      string
	sessionID          string
	previousTurnActive bool
}

func (source terminalOptionsBuilder) options() terminal.Options {
	loadUsage := func(ctx context.Context) (terminal.ProviderUsage, error) {
		usage, err := source.loadUsage(ctx)
		windows := make([]terminal.UsageWindow, len(usage.Windows))
		for index, window := range usage.Windows {
			windows[index] = terminal.UsageWindow{
				Duration:    window.Duration,
				UsedPercent: window.UsedPercent,
				ResetsAt:    window.ResetsAt,
			}
		}
		return terminal.ProviderUsage{
			Windows:           windows,
			MonthlyUsageUSD:   usage.MonthlyUsageUSD,
			LimitRemainingUSD: usage.LimitRemainingUSD,
		}, err
	}
	thinkingLevel, _ := source.settings.Snapshot()
	callbacks := &terminalCallbacks{engine: source.engine, checkpoints: source.checkpoints, skills: source.config.skills}
	return terminal.Options{
		Input:      source.runtime.stdin,
		Output:     source.runtime.stdout,
		Operations: callbacks.operations(),
		Controls: terminal.Controls{
			Steer:            source.engine.Steer,
			ClearSteering:    source.engine.ClearSteering,
			SetGoal:          source.goal.Set,
			Goal:             source.goal.State,
			ClearGoal:        source.goal.Clear,
			SetThinkingLevel: source.settings.SetThinkingLevel,
			SetFastMode: func(enabled bool) error {
				source.settings.SetFastMode(enabled)
				return nil
			},
		},
		Sessions:       source.sessions,
		StateChanges:   terminalStateChanges(source.checkpoints),
		MessageHistory: source.messageHistory,
		Config: terminal.Config{
			Model:              source.config.models.Main,
			WorkingDirectory:   source.config.cwd,
			ThinkingLevel:      thinkingLevel,
			ThinkingLevels:     source.metadata.ThinkingLevels,
			FastMode:           source.config.fastMode,
			FastModeAvailable:  source.metadata.FastMode,
			ContextWindow:      source.metadata.ContextWindow,
			Skills:             source.config.skills,
			Warnings:           source.warnings,
			InitialCheckpoint:  source.initialCheckpoint,
			SessionID:          source.sessionID,
			PreviousTurnActive: source.previousTurnActive,
			InitialPrompt:      source.initialPrompt,
		},
		Events: terminal.Events{
			Interrupts:         source.runtime.interrupts,
			Subagents:          source.subagents,
			PermissionRequests: source.permissionRequests,
		},
		Services: terminal.Services{LoadUsage: loadUsage},
	}
}

type terminalCallbacks struct {
	engine      *agent.Engine
	checkpoints *checkpointCoordinator
	skills      []skill.Skill
}

func (callbacks *terminalCallbacks) operations() terminal.Operations {
	return terminal.Operations{
		RunTurn: func(ctx context.Context, content []agent.ContentPart, stream terminal.EventStream) error {
			content, err := expandSkillContent(content, callbacks.skills)
			if err != nil {
				return err
			}
			_, err = callbacks.engine.Run(ctx, content, callbacks.sink(stream))
			return err
		},
		Compact: func(ctx context.Context, stream terminal.EventStream) error {
			return callbacks.engine.Compact(ctx, callbacks.sink(stream))
		},
	}
}

func expandSkillContent(content []agent.ContentPart, skills []skill.Skill) ([]agent.ContentPart, error) {
	for index, part := range content {
		switch {
		case part.Kind == agent.ContentPartImage:
			return content, nil
		case strings.TrimSpace(part.Text) == "":
			continue
		}

		expanded, err := skill.Expand(part.Text, skills)
		if err != nil {
			return nil, err
		}
		content = slices.Clone(content)
		content[index].Text = expanded
		return content, nil
	}
	return content, nil
}

func (callbacks *terminalCallbacks) sink(stream terminal.EventStream) agent.EventSink {
	return func(event agent.Event) error {
		if event.Kind != agent.EventCheckpoint || event.Checkpoint == nil || callbacks.checkpoints == nil {
			return stream.Emit(event)
		}

		terminalCheckpoint, err := stream.Snapshot()
		if err != nil {
			return err
		}
		return callbacks.checkpoints.SaveSessionCheckpoint(*event.Checkpoint, terminalCheckpoint, true)
	}
}

func terminalStateChanges(checkpoints *checkpointCoordinator) terminal.StateChanges {
	if checkpoints == nil {
		return terminal.StateChanges{}
	}
	return terminal.StateChanges{Notify: checkpoints.SaveTerminalCheckpoint}
}
