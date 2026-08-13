package interactive

import (
	"context"
	"fmt"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
)

type sessionPersistence struct {
	handle      *sessionHandle
	checkpoints *checkpointCoordinator
	changesDone chan struct{}
}

func newStoredAgentSession(
	config resolvedConfig,
	env environment,
	backendRuntime backend.Runtime,
	store *sessionStore,
	handle *sessionHandle,
) (*agentSession, error) {
	session, err := newAgentSessionWithCheckpointing(config, env, backendRuntime, true)
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
	if err := session.attachPersistence(handle, restore); err != nil {
		return nil, session.finish(err)
	}
	return session, nil
}

func (session *agentSession) attachPersistence(handle *sessionHandle, restore bool) error {
	checkpoints := newCheckpointCoordinator(
		handle,
		session.engine,
		session.subagents,
		session.settings,
	)
	persistence := &sessionPersistence{
		handle:      handle,
		checkpoints: checkpoints,
		changesDone: make(chan struct{}),
	}
	session.persistence = persistence

	record := handle.Record()
	session.terminalOptions.Config.SessionID = record.ID
	if restore {
		if err := session.engine.RestoreCheckpoint(record.Agent); err != nil {
			return fmt.Errorf("restore agent session: %w", err)
		}
		if err := session.subagents.RestoreCheckpoint(record.Subagent); err != nil {
			return fmt.Errorf("restore subagents: %w", err)
		}
		checkpoint := record.Terminal
		session.terminalOptions.Config.InitialCheckpoint = &checkpoint
		session.terminalOptions.Config.PreviousTurnActive = record.Status == sessionActive
		if err := checkpoints.RestoreIdle(record.Agent); err != nil {
			return fmt.Errorf("save restored subagents: %w", err)
		}
	}

	session.terminalCallbacks.attachPersistence(checkpoints, func(context.Context) ([]terminal.SessionSummary, []string, error) {
		summaries, warnings, err := handle.store.List(record.WorkingDirectory)
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
	})
	session.terminalOptions.Sessions = session.terminalCallbacks.sessions
	session.terminalOptions.Checkpoints = session.terminalCallbacks.checkpointsPort
	go func() {
		defer close(persistence.changesDone)
		for range session.subagents.CheckpointChanges() {
			checkpoints.SaveIdle()
		}
	}()

	return nil
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
