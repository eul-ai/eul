package interactive

import (
	"context"
	"errors"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal"
)

type terminalCommands struct {
	setThinkingLevel func(agent.ThinkingLevel) error
	setFastMode      func(bool) error
	saveCheckpoint   func(agent.Checkpoint, terminal.Checkpoint, bool) error
	listSessions     func(context.Context) ([]terminal.SessionSummary, []string, error)
}

func (commands *terminalCommands) SetThinkingLevel(level agent.ThinkingLevel) error {
	if commands.setThinkingLevel == nil {
		return errors.New("thinking level selection is unavailable")
	}
	return commands.setThinkingLevel(level)
}

func (commands *terminalCommands) SetFastMode(enabled bool) error {
	if commands.setFastMode == nil {
		return errors.New("fast mode is unavailable")
	}
	return commands.setFastMode(enabled)
}

func (commands *terminalCommands) SaveCheckpoint(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint, active bool) error {
	if commands.saveCheckpoint == nil {
		return errors.New("checkpointing is unavailable")
	}
	return commands.saveCheckpoint(agentCheckpoint, terminalCheckpoint, active)
}

func (commands *terminalCommands) ListSessions(ctx context.Context) ([]terminal.SessionSummary, []string, error) {
	if commands.listSessions == nil {
		return nil, nil, errors.New("session resumption is unavailable")
	}
	return commands.listSessions(ctx)
}

func (commands *terminalCommands) CanSetThinkingLevel() bool { return commands.setThinkingLevel != nil }
func (commands *terminalCommands) CanSetFastMode() bool      { return commands.setFastMode != nil }
func (commands *terminalCommands) CanSaveCheckpoint() bool   { return commands.saveCheckpoint != nil }
func (commands *terminalCommands) CanListSessions() bool     { return commands.listSessions != nil }
