package app

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
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
	conversation       *session.Session
	sessions           terminal.Sessions
	messageHistory     terminal.MessageHistory
	draft              session.Draft
	contextTokens      int64
	initialPrompt      string
	sessionID          string
}

func (source terminalOptionsBuilder) options() terminal.Options {
	thinkingLevel, _ := source.settings.Snapshot()
	return terminal.Options{
		Operations: terminal.Operations{
			RunTurn: func(ctx context.Context, content []agent.ContentPart, sink agent.EventSink) error {
				content, err := expandSkillContent(content, source.config.skills)
				if err != nil {
					return err
				}
				_, err = source.engine.Run(ctx, content, sink)
				return err
			},
			Compact: source.engine.Compact,
		},
		Controls: terminal.Controls{
			Steer:            source.engine.Steer,
			ClearSteering:    source.engine.ClearSteering,
			SetGoal:          source.goal.Set,
			Goal:             source.goal.State,
			ClearGoal:        source.goal.Clear,
			SetThinkingLevel: source.settings.SetThinkingLevel,
			SetFastMode:      source.settings.SetFastMode,
		},
		Session:        source.conversation,
		Sessions:       source.sessions,
		MessageHistory: source.messageHistory,
		Config: terminal.Config{
			Model:             source.config.Models.Main,
			WorkingDirectory:  source.config.WorkingDirectory,
			ThinkingLevel:     thinkingLevel,
			ThinkingLevels:    source.metadata.ThinkingLevels,
			FastMode:          source.config.FastMode,
			FastModeAvailable: source.metadata.FastMode,
			ContextWindow:     source.metadata.ContextWindow,
			ContextTokens:     source.contextTokens,
			Skills:            source.config.skills,
			Draft:             source.draft,
			SessionID:         source.sessionID,
			InitialPrompt:     source.initialPrompt,
		},
		Events: terminal.Events{
			Interrupts:         source.interrupts,
			Subagents:          source.subagents,
			PermissionRequests: source.permissionRequests,
		},
		Services: terminal.Services{LoadUsage: source.loadUsage},
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
