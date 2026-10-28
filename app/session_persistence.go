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

type sessionSaver struct {
	mu        sync.Mutex
	handle    *sessionHandle
	engine    *agent.Engine
	subagents *subagent.Manager
	settings  *agent.Settings
	stopWatch chan struct{}
	watchDone chan struct{}
}

func newStoredAgentSession(
	launcher *launcher,
	config resolvedConfig,
	runtime *backend.Runtime,
	handle *sessionHandle,
	prompt string,
) (*agentSession, error) {
	record := handle.Record()
	historyEntries, err := launcher.history.Load(record.ID)
	if err != nil {
		return nil, fmt.Errorf("load message history: %w", err)
	}
	session, options, err := newAgentSessionComponents(config, runtime, record, launcher.noSandbox, launcher.interrupts)
	if err != nil {
		return nil, err
	}

	session.saver = &sessionSaver{
		handle:    handle,
		engine:    session.engine,
		subagents: session.subagents,
		settings:  session.agentSettings,
		stopWatch: make(chan struct{}),
		watchDone: make(chan struct{}),
	}
	go session.saver.watchSubagents()

	options.saver = session.saver
	options.messageHistory = terminal.MessageHistory{
		Entries: historyEntries,
		Append: func(entry string) error {
			return launcher.history.Append(record.ID, entry)
		},
	}
	options.initialCheckpoint = &record.Terminal
	options.previousTurnActive = record.Status == sessionActive
	options.contextTokens = record.Agent.ContextUsage.TotalTokens
	options.initialPrompt = prompt
	options.sessionID = record.ID
	options.sessions.List = func(context.Context) ([]terminal.SessionSummary, error) {
		return handle.store.List(record.WorkingDirectory)
	}
	session.terminalOptions = options.options()

	return session, nil
}

func (saver *sessionSaver) watchSubagents() {
	defer close(saver.watchDone)
	changes := saver.subagents.Changes()
	for {
		select {
		case <-changes:
			changes = saver.subagents.Changes()
			saver.saveIdle()
		case <-saver.stopWatch:
			return
		}
	}
}

func (saver *sessionSaver) saveTurn(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint) error {
	saver.mu.Lock()
	defer saver.mu.Unlock()
	return saver.saveLocked(agentCheckpoint, terminalCheckpoint, true)
}

func (saver *sessionSaver) saveTerminal(terminalCheckpoint terminal.Checkpoint, active bool) error {
	agentCheckpoint, err := saver.engine.Checkpoint()
	if err != nil {
		return err
	}

	saver.mu.Lock()
	defer saver.mu.Unlock()
	return saver.saveLocked(agentCheckpoint, terminalCheckpoint, active)
}

// saveIdle persists subagent changes between turns. Its errors are not
// reported, because every later save rewrites the whole session.
func (saver *sessionSaver) saveIdle() {
	saver.mu.Lock()
	defer saver.mu.Unlock()

	record := saver.handle.Record()
	if record.Status == sessionActive {
		return
	}
	_ = saver.saveLocked(record.Agent, record.Terminal, false)
}

func (saver *sessionSaver) saveLocked(agentCheckpoint agent.Checkpoint, terminalCheckpoint terminal.Checkpoint, active bool) error {
	thinkingLevel, fastMode := saver.settings.Snapshot()
	return saver.handle.Save(agentCheckpoint, saver.subagents.Checkpoint(), terminalCheckpoint, active, thinkingLevel, fastMode)
}

func (saver *sessionSaver) stop() {
	close(saver.stopWatch)
	<-saver.watchDone
}

func (saver *sessionSaver) close() error {
	saver.stop()

	saver.mu.Lock()
	defer saver.mu.Unlock()
	agentCheckpoint, err := saver.engine.Checkpoint()
	if err == nil {
		err = saver.saveLocked(agentCheckpoint, saver.handle.Record().Terminal, false)
	}
	return errors.Join(err, saver.handle.Close())
}
