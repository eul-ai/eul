package session

import (
	"errors"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

type Saver struct {
	mu        sync.Mutex
	handle    *Handle
	engine    *agent.Engine
	subagents *subagent.Manager
	settings  *agent.Settings
	stopWatch chan struct{}
	watchDone chan struct{}
}

func NewSaver(handle *Handle, engine *agent.Engine, subagents *subagent.Manager, settings *agent.Settings) *Saver {
	saver := &Saver{
		handle:    handle,
		engine:    engine,
		subagents: subagents,
		settings:  settings,
		stopWatch: make(chan struct{}),
		watchDone: make(chan struct{}),
	}
	go saver.watchSubagents()
	return saver
}

func (saver *Saver) watchSubagents() {
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

func (saver *Saver) SaveTurn(agentCheckpoint agent.Checkpoint, transcript []Entry, draft Draft) error {
	saver.mu.Lock()
	defer saver.mu.Unlock()
	return saver.saveLocked(agentCheckpoint, transcript, draft, true)
}

func (saver *Saver) SaveView(transcript []Entry, draft Draft, active bool) error {
	agentCheckpoint, err := saver.engine.Checkpoint()
	if err != nil {
		return err
	}

	saver.mu.Lock()
	defer saver.mu.Unlock()
	return saver.saveLocked(agentCheckpoint, transcript, draft, active)
}

// saveIdle persists subagent changes between turns. Its errors are not
// reported, because every later save rewrites the whole session.
func (saver *Saver) saveIdle() {
	saver.mu.Lock()
	defer saver.mu.Unlock()

	record := saver.handle.Record()
	if record.Active {
		return
	}
	_ = saver.saveLocked(record.Agent, record.Transcript, record.Draft, false)
}

func (saver *Saver) saveLocked(agentCheckpoint agent.Checkpoint, transcript []Entry, draft Draft, active bool) error {
	thinkingLevel, fastMode := saver.settings.Snapshot()
	next := state{Agent: agentCheckpoint, Subagent: saver.subagents.Checkpoint(), Transcript: transcript, Draft: draft}
	return saver.handle.save(next, active, thinkingLevel, fastMode)
}

func (saver *Saver) Close() error {
	close(saver.stopWatch)
	<-saver.watchDone

	saver.mu.Lock()
	defer saver.mu.Unlock()
	record := saver.handle.Record()
	agentCheckpoint, err := saver.engine.Checkpoint()
	if err == nil {
		err = saver.saveLocked(agentCheckpoint, record.Transcript, record.Draft, false)
	}
	return errors.Join(err, saver.handle.Close())
}
