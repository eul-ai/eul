package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

const previousTurnNotice = "Previous session ended during an active turn; tool side effects may remain"

type UpdateKind uint8

const (
	UpdateEvent UpdateKind = iota
	UpdateSubagents
	UpdateSettled
)

// Update reports a change to the UI. Any update may follow transcript
// changes, which Sync picks up.
type Update struct {
	Kind      UpdateKind
	Event     agent.Event
	Subagents subagent.Status
	Err       error
}

type Config struct {
	Handle    *Handle
	Engine    *agent.Engine
	Subagents *subagent.Manager
	Settings  *agent.Settings
	Skills    []skill.Skill
	Warnings  []string
}

type operation func(context.Context, agent.EventSink) error

// Session owns a conversation: it runs its turns, queues the prompts sent
// while a turn runs, and saves the transcript with the agent and subagent
// state. mu guards the transcript, the draft and the turn. saveMu serializes
// saves so that they reach the disk in the order they were taken, without
// holding mu while writing. Updates are never sent while holding either.
//
// queue holds the prompts sent during a turn that the model has not
// received. Its first steered prompts were handed to the engine, which
// delivers them in order.
type Session struct {
	handle    *Handle
	engine    *agent.Engine
	subagents *subagent.Manager
	settings  *agent.Settings
	skills    []skill.Skill
	updates   chan Update
	closing   chan struct{}
	workers   sync.WaitGroup

	saveMu sync.Mutex

	mu          sync.Mutex
	transcript  Transcript
	draft       Draft
	running     bool
	interrupted bool
	cancel      context.CancelFunc
	queue       [][]agent.ContentPart
	steered     int
}

func New(config Config) *Session {
	record := config.Handle.Record()
	s := &Session{
		handle:     config.Handle,
		engine:     config.Engine,
		subagents:  config.Subagents,
		settings:   config.Settings,
		skills:     config.Skills,
		updates:    make(chan Update, 256),
		closing:    make(chan struct{}),
		transcript: restoreTranscript(record.Transcript),
		draft:      record.Draft,
	}
	if record.Active {
		s.transcript.Append(EntryNotice, previousTurnNotice)
	}
	for _, warning := range config.Warnings {
		s.transcript.Append(EntryNotice, warning)
	}
	s.workers.Go(s.watchSubagents)
	return s
}

func (s *Session) Updates() <-chan Update {
	return s.updates
}

// Send starts a turn with content, or queues it when a turn is running.
func (s *Session) Send(content []agent.ContentPart) error {
	if s.enqueue(content) {
		return nil
	}
	if err := s.saveActive(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.AppendUser(content)
	s.startLocked(s.prompt(content))
	return nil
}

func (s *Session) Compact() error {
	if err := s.saveActive(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked(s.engine.Compact)
	return nil
}

// Interrupt cancels the running turn and returns the queued prompts.
func (s *Session) Interrupt() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.interrupted = true
		s.cancel()
	}
	return s.takeQueuedLocked()
}

// TakeQueued removes and returns the prompts the model has not received.
func (s *Session) TakeQueued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeQueuedLocked()
}

func (s *Session) Queued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queue)
}

func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
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

func (s *Session) SetDraft(draft Draft) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draft = draft
}

func (s *Session) SaveDraft(draft Draft) error {
	s.SetDraft(draft)
	agentCheckpoint, err := s.engine.Checkpoint()
	if err != nil {
		return err
	}
	return s.save(agentCheckpoint, false)
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.running {
		s.interrupted = true
		s.cancel()
	}
	s.mu.Unlock()
	close(s.closing)
	s.workers.Wait()

	agentCheckpoint, err := s.engine.Checkpoint()
	if err == nil {
		err = s.save(agentCheckpoint, false)
	}
	return errors.Join(err, s.handle.Close())
}

func (s *Session) enqueue(content []agent.ContentPart) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}

	if s.steered == len(s.queue) && !s.interrupted && s.engine.Steer(content) {
		s.steered++
	}
	s.queue = append(s.queue, content)
	return true
}

// takeQueuedLocked trusts the engine about the steered prompts it has not
// delivered. The others it already took from its queue, and the sink drops
// them from s.queue when their delivery events arrive.
func (s *Session) takeQueuedLocked() [][]agent.ContentPart {
	undelivered := s.engine.ClearSteering()
	delivering := s.steered - len(undelivered)
	taken := append(undelivered, s.queue[s.steered:]...)
	s.queue = s.queue[:delivering]
	s.steered = delivering
	return taken
}

func (s *Session) startLocked(run operation) {
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.interrupted, s.cancel = true, false, cancel
	s.workers.Go(func() { s.run(ctx, run) })
}

func (s *Session) run(ctx context.Context, run operation) {
	for {
		var ok bool
		if ctx, run, ok = s.settle(run(ctx, s.sink)); !ok {
			return
		}
	}
}

// settle finishes a turn. When it succeeded and prompts are queued, settle
// returns the next turn, which runs the first of them.
func (s *Session) settle(err error) (context.Context, operation, bool) {
	agentCheckpoint, checkpointErr := s.engine.Checkpoint()

	s.mu.Lock()
	s.cancel()
	s.transcript.FinishTurn(s.interrupted, err)
	s.steered = 0
	if !s.interrupted && err == nil && len(s.queue) > 0 {
		next := s.queue[0]
		s.queue = slices.Delete(s.queue, 0, 1)
		s.transcript.AppendUser(next)
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.mu.Unlock()

		s.publish(Update{Kind: UpdateEvent, Event: agent.Event{Kind: agent.EventSteering, Content: next}})
		return ctx, func(ctx context.Context, sink agent.EventSink) error {
			if err := s.saveActive(); err != nil {
				return err
			}
			return s.prompt(next)(ctx, sink)
		}, true
	}
	if s.interrupted {
		err = nil
	}
	s.running, s.interrupted = false, false
	s.mu.Unlock()

	if checkpointErr == nil {
		checkpointErr = s.save(agentCheckpoint, false)
	}
	s.publish(Update{Kind: UpdateSettled, Err: errors.Join(err, checkpointErr)})
	return nil, nil, false
}

func (s *Session) prompt(content []agent.ContentPart) operation {
	return func(ctx context.Context, sink agent.EventSink) error {
		content, err := expandSkill(content, s.skills)
		if err != nil {
			return err
		}
		_, err = s.engine.Run(ctx, content, sink)
		return err
	}
}

func (s *Session) sink(event agent.Event) error {
	if event.Kind == agent.EventCheckpoint {
		return s.save(*event.Checkpoint, true)
	}

	s.mu.Lock()
	s.transcript.Apply(event)
	if event.Kind == agent.EventSteering {
		s.queue = slices.Delete(s.queue, 0, 1)
		s.steered--
	}
	s.mu.Unlock()
	s.publish(Update{Kind: UpdateEvent, Event: event})
	return nil
}

func (s *Session) publish(update Update) {
	select {
	case s.updates <- update:
	case <-s.closing:
	}
}

func (s *Session) watchSubagents() {
	changes := s.subagents.Changes()
	s.publish(Update{Kind: UpdateSubagents, Subagents: s.subagents.Snapshot()})
	for {
		select {
		case <-changes:
			changes = s.subagents.Changes()
			s.saveIdle()
			s.publish(Update{Kind: UpdateSubagents, Subagents: s.subagents.Snapshot()})
		case <-s.closing:
			return
		}
	}
}

func (s *Session) saveActive() error {
	agentCheckpoint, err := s.engine.Checkpoint()
	if err != nil {
		return err
	}
	return s.save(agentCheckpoint, true)
}

func (s *Session) save(agentCheckpoint agent.Checkpoint, active bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveLocked(agentCheckpoint, active)
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
	draft := s.draft
	draft.QueuedInputs = queuedInputs(s.queue)
	next := state{Agent: agentCheckpoint, Transcript: s.transcript.persisted(), Draft: draft}
	s.mu.Unlock()

	next.Subagent = s.subagents.Checkpoint()
	thinkingLevel, fastMode := s.settings.Snapshot()
	return s.handle.save(next, active, thinkingLevel, fastMode)
}

func queuedInputs(queue [][]agent.ContentPart) []string {
	var inputs []string
	for _, content := range queue {
		if text := contentText(content); strings.TrimSpace(text) != "" {
			inputs = append(inputs, text)
		}
	}
	return inputs
}

func expandSkill(content []agent.ContentPart, skills []skill.Skill) ([]agent.ContentPart, error) {
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
