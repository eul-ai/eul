package terminal

import (
	"context"

	"github.com/eul-ai/eul/agent"
)

type testCommands struct {
	setThinkingLevel func(agent.ThinkingLevel) error
	setFastMode      func(bool) error
	saveCheckpoint   func(agent.Checkpoint, Checkpoint, bool) error
	listSessions     func(context.Context) ([]SessionSummary, []string, error)
}

func (commands testCommands) SetThinkingLevel(level agent.ThinkingLevel) error {
	return commands.setThinkingLevel(level)
}

func (commands testCommands) SetFastMode(enabled bool) error {
	return commands.setFastMode(enabled)
}

func (commands testCommands) SaveCheckpoint(agentCheckpoint agent.Checkpoint, terminalCheckpoint Checkpoint, active bool) error {
	return commands.saveCheckpoint(agentCheckpoint, terminalCheckpoint, active)
}

func (commands testCommands) ListSessions(ctx context.Context) ([]SessionSummary, []string, error) {
	return commands.listSessions(ctx)
}

func (commands testCommands) CanSetThinkingLevel() bool { return commands.setThinkingLevel != nil }
func (commands testCommands) CanSetFastMode() bool      { return commands.setFastMode != nil }
func (commands testCommands) CanSaveCheckpoint() bool   { return commands.saveCheckpoint != nil }
func (commands testCommands) CanListSessions() bool     { return commands.listSessions != nil }
