package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type sessionPersistence struct {
	handle      *sessionHandle
	checkpoints *checkpointCoordinator
	changesDone chan struct{}
}

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

func newStoredAgentSession(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	store *sessionStore,
	messageHistory *messageHistoryStore,
	handle *sessionHandle,
) (*agentSession, error) {
	session, options, err := newAgentSessionComponents(config, env, backendRuntime, true)
	if err != nil {
		if handle != nil {
			_ = handle.Close()
		}
		return nil, err
	}

	restore := handle != nil
	if handle == nil {
		agentCheckpoint, checkpointErr := session.engine.Checkpoint()
		if checkpointErr != nil {
			return nil, session.finish(checkpointErr)
		}
		thinkingLevel, fastMode := session.settings.Snapshot()
		handle, err = store.Create(
			config.provider,
			config.cwd,
			config.models,
			thinkingLevel,
			agentCheckpoint,
			session.subagents.Checkpoint(),
			terminal.EmptyCheckpoint(),
			fastMode,
		)
		if err != nil {
			return nil, session.finish(err)
		}
	}

	record := handle.Record()
	if restore {
		if err := session.engine.RestoreCheckpoint(record.Agent); err != nil {
			return nil, session.finish(fmt.Errorf("restore agent session: %w", err))
		}
		if err := session.subagents.RestoreCheckpoint(record.Subagent); err != nil {
			return nil, session.finish(fmt.Errorf("restore subagents: %w", err))
		}
	}

	historyEntries, err := messageHistory.Load(record.ID)
	if err != nil {
		return nil, errors.Join(session.finish(fmt.Errorf("load message history: %w", err)), handle.Close())
	}
	options.messageHistory = terminal.MessageHistory{
		Entries: historyEntries,
		Append: func(prompt string) error {
			return messageHistory.Append(record.ID, prompt)
		},
	}

	checkpoints := newCheckpointCoordinator(handle, session.engine, session.subagents, session.settings)
	persistence := &sessionPersistence{
		handle:      handle,
		checkpoints: checkpoints,
		changesDone: make(chan struct{}),
	}
	session.persistence = persistence

	if restore {
		if err := checkpoints.RestoreIdle(record.Agent); err != nil {
			return nil, session.finish(fmt.Errorf("save restored subagents: %w", err))
		}
		checkpoint := record.Terminal
		options.initialCheckpoint = &checkpoint
		options.previousTurnActive = record.Status == sessionActive
	}
	options.checkpoints = checkpoints
	options.sessionID = record.ID
	options.sessions.List = sessionList(handle)
	session.terminalOptions = options.options()

	go func() {
		defer close(persistence.changesDone)
		for range session.subagents.CheckpointChanges() {
			checkpoints.SaveIdle()
		}
	}()

	return session, nil
}

func (coordinator *checkpointCoordinator) SaveSessionCheckpoint(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint, active bool) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return errors.New("session is closed")
	}

	coordinator.agent = agentCheckpoint
	coordinator.terminal = terminalCheckpoint
	return coordinator.saveStateLocked(active)
}

func (coordinator *checkpointCoordinator) SaveTerminalCheckpoint(terminalCheckpoint terminal.Checkpoint, active bool) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return errors.New("session is closed")
	}

	agentCheckpoint, err := coordinator.engine.Checkpoint()
	if err != nil {
		return err
	}
	coordinator.agent = agentCheckpoint
	coordinator.terminal = terminalCheckpoint
	return coordinator.saveStateLocked(active)
}

func (coordinator *checkpointCoordinator) saveStateLocked(active bool) error {
	coordinator.active = active
	if err := coordinator.saveLocked(coordinator.agent); err != nil {
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

func sessionList(handle *sessionHandle) func(context.Context) ([]terminal.SessionSummary, []string, error) {
	return func(context.Context) ([]terminal.SessionSummary, []string, error) {
		summaries, warnings, err := handle.store.List(handle.record.WorkingDirectory)
		if err != nil {
			return nil, nil, err
		}
		visible := make([]terminal.SessionSummary, 0, len(summaries))
		for _, summary := range summaries {
			visible = append(visible, terminal.SessionSummary{
				ID:          summary.ID,
				Description: summary.Description,
				UpdatedAt:   summary.UpdatedAt,
				Active:      summary.Active,
			})
		}
		return visible, warnings, nil
	}
}

func (persistence *sessionPersistence) stop() {
	persistence.checkpoints.Stop()
}

func (persistence *sessionPersistence) saveFinal() error {
	<-persistence.changesDone
	return persistence.checkpoints.SaveFinal()
}

func (persistence *sessionPersistence) close() error {
	return persistence.checkpoints.Close()
}
