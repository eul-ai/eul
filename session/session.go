package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

const previousTurnNotice = "Previous session ended during an active turn; tool side effects may remain"

type UpdateKind uint8

const (
	UpdateEvent UpdateKind = iota
	UpdateSubagents
	UpdatePermissions
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
	Handle        *Handle
	Store         *Store
	History       *History
	AgentSettings *agent.Settings
	Goal          *agent.Goal
	Model         backend.ModelMetadata
	Usage         func(context.Context) (backend.AccountUsage, error)
	Skills        []skill.Skill
	Warnings      []string
	Prompt        string
}

// Info describes the session as it was opened.
type Info struct {
	ID            string
	Model         backend.ModelMetadata
	ContextTokens int64
	Skills        []skill.Skill
	History       []string
	Draft         Draft
	Prompt        string
}

// Session owns a conversation: it runs its turns, queues the prompts sent
// while a turn runs, and saves the transcript with the agent and subagent
// state. mu guards the transcript, the draft, the turn and the permission
// requests. saveMu serializes saves so that they reach the disk in the
// order they were taken, without holding mu while writing. Updates are
// never sent while holding either.
//
// queue holds the prompts sent during a turn that the model has not
// received. Its first steered prompts were handed to the engine, which
// delivers them in order.
type Session struct {
	info          Info
	settings      Settings
	handle        *Handle
	store         *Store
	history       *History
	agentSettings *agent.Settings
	goal          *agent.Goal
	usage         func(context.Context) (backend.AccountUsage, error)
	skills        []skill.Skill
	engine        *agent.Engine
	subagents     *subagent.Manager
	updates       chan Update
	closing       chan struct{}
	workers       sync.WaitGroup

	saveMu sync.Mutex

	mu             sync.Mutex
	transcript     Transcript
	draft          Draft
	running        bool
	interrupted    bool
	cancel         context.CancelFunc
	queue          [][]agent.ContentPart
	steered        int
	permissions    []*PermissionRequest
	networkAllowed bool
}

// New opens a session around a stored conversation. Its tools may use
// AuthorizeNetwork before Start hands it the engine and subagents that run
// them.
func New(config Config) (*Session, error) {
	record := config.Handle.Record()
	history, err := config.History.Load(record.ID)
	if err != nil {
		return nil, fmt.Errorf("load message history: %w", err)
	}

	s := &Session{
		info: Info{
			ID:            record.ID,
			Model:         config.Model,
			ContextTokens: record.Agent.ContextUsage.TotalTokens,
			Skills:        config.Skills,
			History:       history,
			Draft:         record.Draft,
			Prompt:        config.Prompt,
		},
		settings:      record.Settings,
		handle:        config.Handle,
		store:         config.Store,
		history:       config.History,
		agentSettings: config.AgentSettings,
		goal:          config.Goal,
		usage:         config.Usage,
		skills:        config.Skills,
		updates:       make(chan Update, 256),
		closing:       make(chan struct{}),
		transcript:    restoreTranscript(record.Transcript),
		draft:         record.Draft,
	}
	if record.Active {
		s.transcript.Append(EntryNotice, previousTurnNotice)
	}
	for _, warning := range config.Warnings {
		s.transcript.Append(EntryNotice, warning)
	}
	return s, nil
}

func (s *Session) Start(engine *agent.Engine, subagents *subagent.Manager) {
	s.engine = engine
	s.subagents = subagents
	s.workers.Go(s.watchSubagents)
}

func (s *Session) Info() Info {
	return s.info
}

func (s *Session) Settings() Settings {
	settings := s.settings
	settings.ThinkingLevel, settings.FastMode = s.agentSettings.Snapshot()
	return settings
}

func (s *Session) SetThinkingLevel(level agent.ThinkingLevel) error {
	return s.agentSettings.SetThinkingLevel(level)
}

func (s *Session) SetFastMode(enabled bool) {
	s.agentSettings.SetFastMode(enabled)
}

func (s *Session) Goal() (agent.GoalState, bool) {
	return s.goal.State()
}

func (s *Session) SetGoal(objective string) error {
	return s.goal.Set(objective)
}

func (s *Session) ClearGoal() {
	s.goal.Clear()
}

// Remember adds a prompt to the message history.
func (s *Session) Remember(prompt string) error {
	return s.history.Append(s.info.ID, prompt)
}

// Sessions lists the other sessions in the working directory.
func (s *Session) Sessions() ([]Summary, error) {
	return s.store.List(s.settings.WorkingDirectory)
}

func (s *Session) Usage(ctx context.Context) (backend.AccountUsage, error) {
	return s.usage(ctx)
}

func (s *Session) Updates() <-chan Update {
	return s.updates
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
	s.subagents.Close()

	agentCheckpoint, err := s.engine.Checkpoint()
	if err == nil {
		err = s.save(agentCheckpoint, false)
	}
	return errors.Join(err, s.handle.Close())
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
	thinkingLevel, fastMode := s.agentSettings.Snapshot()
	return s.handle.save(next, active, thinkingLevel, fastMode)
}
