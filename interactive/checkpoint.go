package interactive

import (
	"errors"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type checkpointCoordinator struct {
	mu        sync.Mutex
	handle    *sessionHandle
	engine    *agent.Engine
	subagents *subagent.Manager
	agent     agent.Checkpoint
	terminal  terminal.Checkpoint
	settings  *agent.Settings
	idleErr   error
	active    bool
	closed    bool
}

func newCheckpointCoordinator(
	handle *sessionHandle,
	engine *agent.Engine,
	subagents *subagent.Manager,
	settings *agent.Settings,
) *checkpointCoordinator {
	return &checkpointCoordinator{
		handle:    handle,
		engine:    engine,
		subagents: subagents,
		agent:     handle.record.Agent,
		terminal:  handle.record.Terminal,
		settings:  settings,
		active:    handle.record.Status == sessionActive,
	}
}

func (coordinator *checkpointCoordinator) SaveSessionCheckpoint(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint, active bool) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return errors.New("session is closed")
	}

	coordinator.agent = agentCheckpoint
	coordinator.terminal = terminalCheckpoint
	coordinator.active = active
	if err := coordinator.saveLocked(agentCheckpoint); err != nil {
		return err
	}
	if !active {
		coordinator.idleErr = nil
	}
	return nil
}

func (coordinator *checkpointCoordinator) SaveIdle() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed || coordinator.active {
		return
	}

	if err := coordinator.saveLocked(coordinator.agent); err != nil {
		coordinator.idleErr = errors.Join(coordinator.idleErr, err)
	}
}

func (coordinator *checkpointCoordinator) RestoreIdle(agentCheckpoint agent.Checkpoint) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.agent = agentCheckpoint
	coordinator.active = false
	return coordinator.saveLocked(agentCheckpoint)
}

func (coordinator *checkpointCoordinator) Stop() {
	coordinator.mu.Lock()
	coordinator.closed = true
	coordinator.mu.Unlock()
}

func (coordinator *checkpointCoordinator) SaveFinal() error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	agentCheckpoint, err := coordinator.engine.Checkpoint()
	if err != nil {
		return errors.Join(coordinator.idleErr, err)
	}
	coordinator.agent = agentCheckpoint
	coordinator.active = false
	return errors.Join(coordinator.idleErr, coordinator.saveLocked(agentCheckpoint))
}

func (coordinator *checkpointCoordinator) saveLocked(agentCheckpoint agent.Checkpoint) error {
	thinkingLevel, fastMode := coordinator.settings.Snapshot()
	return coordinator.handle.Save(
		agentCheckpoint,
		coordinator.subagents.Checkpoint(),
		coordinator.terminal,
		coordinator.active,
		thinkingLevel,
		fastMode,
	)
}

func (coordinator *checkpointCoordinator) Close() error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.closed = true
	return coordinator.handle.Close()
}
