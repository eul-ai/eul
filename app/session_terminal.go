package app

import (
	"context"
	"os"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal"
)

type terminalOptionsBuilder struct {
	config         resolvedConfig
	interrupts     <-chan os.Signal
	metadata       backend.ModelMetadata
	loadUsage      func(context.Context) (backend.AccountUsage, error)
	settings       *agent.Settings
	goal           *agent.Goal
	conversation   *session.Session
	sessions       terminal.Sessions
	messageHistory terminal.MessageHistory
	draft          session.Draft
	contextTokens  int64
	initialPrompt  string
	sessionID      string
}

func (source terminalOptionsBuilder) options() terminal.Options {
	thinkingLevel, _ := source.settings.Snapshot()
	return terminal.Options{
		Controls: terminal.Controls{
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
		Events:   terminal.Events{Interrupts: source.interrupts},
		Services: terminal.Services{LoadUsage: source.loadUsage},
	}
}
