package session

import (
	"errors"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

const previousTurnNotice = "Previous session ended during an active turn; tool side effects may remain"

// Session owns a conversation's transcript and saves it with the agent and
// subagent state. mu guards the transcript and draft. saveMu serializes
// saves so that they reach the disk in the order they were taken, without
// holding mu while writing.
type Session struct {
	handle    *Handle
	engine    *agent.Engine
	subagents *subagent.Manager
	settings  *agent.Settings
	stopWatch chan struct{}
	watchDone chan struct{}

	saveMu sync.Mutex

	mu         sync.Mutex
	transcript Transcript
	draft      Draft
}

func New(handle *Handle, engine *agent.Engine, subagents *subagent.Manager, settings *agent.Settings, warnings []string) *Session {
	record := handle.Record()
	s := &Session{
		handle:     handle,
		engine:     engine,
		subagents:  subagents,
		settings:   settings,
		stopWatch:  make(chan struct{}),
		watchDone:  make(chan struct{}),
		transcript: restoreTranscript(record.Transcript),
		draft:      record.Draft,
	}
	if record.Active {
		s.transcript.Append(EntryNotice, previousTurnNotice)
	}
	for _, warning := range warnings {
		s.transcript.Append(EntryNotice, warning)
	}
	go s.watchSubagents()
	return s
}

func (s *Session) Sink(next agent.EventSink) agent.EventSink {
	return func(event agent.Event) error {
		if event.Kind == agent.EventCheckpoint {
			return s.saveTurn(*event.Checkpoint)
		}

		s.mu.Lock()
		s.transcript.Apply(event)
		s.mu.Unlock()
		return next(event)
	}
}

func (s *Session) Sync(entries []Entry) ([]Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript.Sync(entries)
}

func (s *Session) Append(kind EntryKind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.Append(kind, text)
}

func (s *Session) AppendUser(content []agent.ContentPart) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.AppendUser(content)
}

func (s *Session) FinishTurn(interrupted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.FinishTurn(interrupted, err)
}

func (s *Session) SetDraft(draft Draft) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draft = draft
}

func (s *Session) SaveDraft(draft Draft, active bool) error {
	s.SetDraft(draft)
	agentCheckpoint, err := s.engine.Checkpoint()
	if err != nil {
		return err
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveLocked(agentCheckpoint, active)
}

func (s *Session) Close() error {
	close(s.stopWatch)
	<-s.watchDone

	agentCheckpoint, err := s.engine.Checkpoint()
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err == nil {
		err = s.saveLocked(agentCheckpoint, false)
	}
	return errors.Join(err, s.handle.Close())
}

func (s *Session) watchSubagents() {
	defer close(s.watchDone)
	changes := s.subagents.Changes()
	for {
		select {
		case <-changes:
			changes = s.subagents.Changes()
			s.saveIdle()
		case <-s.stopWatch:
			return
		}
	}
}

func (s *Session) saveTurn(agentCheckpoint agent.Checkpoint) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveLocked(agentCheckpoint, true)
}

// saveIdle persists subagent changes between turns. Its errors are not
// reported, because every later save rewrites the whole session.
func (s *Session) saveIdle() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	record := s.handle.Record()
	if record.Active {
		return
	}
	_ = s.saveLocked(record.Agent, false)
}

func (s *Session) saveLocked(agentCheckpoint agent.Checkpoint, active bool) error {
	s.mu.Lock()
	next := state{Agent: agentCheckpoint, Transcript: s.transcript.persisted(), Draft: s.draft}
	s.mu.Unlock()

	next.Subagent = s.subagents.Checkpoint()
	thinkingLevel, fastMode := s.settings.Snapshot()
	return s.handle.save(next, active, thinkingLevel, fastMode)
}
