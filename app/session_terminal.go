package app

import (
	"context"
	"os"
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
	interrupts         <-chan os.Signal
	metadata           backend.ModelMetadata
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
	contextTokens      int64
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
		Operations: callbacks.operations(),
		Controls: terminal.Controls{
			Steer:            source.engine.Steer,
			ClearSteering:    source.engine.ClearSteering,
			SetGoal:          source.goal.Set,
			Goal:             source.goal.State,
			ClearGoal:        source.goal.Clear,
			SetThinkingLevel: source.settings.SetThinkingLevel,
			SetFastMode:      source.settings.SetFastMode,
		},
		Sessions:       source.sessions,
		StateChanges:   terminal.StateChanges{Notify: source.checkpoints.SaveTerminalCheckpoint},
		MessageHistory: source.messageHistory,
		Config: terminal.Config{
			Model:              source.config.models.Main,
			WorkingDirectory:   source.config.cwd,
			ThinkingLevel:      thinkingLevel,
			ThinkingLevels:     source.metadata.ThinkingLevels,
			FastMode:           source.config.fastMode,
			FastModeAvailable:  source.metadata.FastMode,
			ContextWindow:      source.metadata.ContextWindow,
			ContextTokens:      source.contextTokens,
			Skills:             source.config.skills,
			Warnings:           source.config.warnings,
			InitialCheckpoint:  source.initialCheckpoint,
			SessionID:          source.sessionID,
			PreviousTurnActive: source.previousTurnActive,
			InitialPrompt:      source.initialPrompt,
		},
		Events: terminal.Events{
			Interrupts:         source.interrupts,
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
		if event.Kind != agent.EventCheckpoint {
			return stream.Emit(event)
		}

		terminalCheckpoint, err := stream.Snapshot()
		if err != nil {
			return err
		}
		return callbacks.checkpoints.SaveSessionCheckpoint(*event.Checkpoint, terminalCheckpoint, true)
	}
}
