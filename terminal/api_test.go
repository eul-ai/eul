package terminal

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
)

// fakeSession records what the terminal asks of a session. Tests play the
// session's side by sending the updates that event and settle return.
type fakeSession struct {
	mu          sync.Mutex
	info        session.Info
	settings    session.Settings
	transcript  session.Transcript
	updates     chan session.Update
	calls       chan string
	running     bool
	interrupted bool
	queue       [][]agent.ContentPart
	sent        [][]agent.ContentPart
	compactions int
	startErr    error
	save        func(session.Draft) error
	permissions []*session.PermissionRequest
	decisions   []session.PermissionDecision
	thinkingErr error
	goal        *agent.GoalState
	goalErr     error
	remembered  []string
	summaries   []session.Summary
	usage       func(context.Context) (backend.AccountUsage, error)
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		settings: session.Settings{ThinkingLevel: agent.DefaultThinkingLevel},
		updates:  make(chan session.Update, 64),
	}
}

func (s *fakeSession) Info() session.Info {
	return s.info
}

func (s *fakeSession) Settings() session.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *fakeSession) SetThinkingLevel(level agent.ThinkingLevel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.thinkingErr != nil {
		return s.thinkingErr
	}
	s.settings.ThinkingLevel = level
	return nil
}

func (s *fakeSession) SetFastMode(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.FastMode = enabled
}

func (s *fakeSession) Goal() (agent.GoalState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal == nil {
		return agent.GoalState{}, false
	}
	return *s.goal, true
}

func (s *fakeSession) SetGoal(objective string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goalErr != nil {
		return s.goalErr
	}
	s.goal = &agent.GoalState{Objective: objective}
	return nil
}

func (s *fakeSession) ClearGoal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goal = nil
}

func (s *fakeSession) Updates() <-chan session.Update {
	return s.updates
}

func (s *fakeSession) Send(content []agent.ContentPart) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.queue = append(s.queue, content)
		return nil
	}
	if s.startErr != nil {
		return s.startErr
	}
	s.sent = append(s.sent, content)
	s.transcript.AppendUser(content)
	s.startLocked()
	return nil
}

func (s *fakeSession) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startErr != nil {
		return s.startErr
	}
	s.compactions++
	s.startLocked()
	return nil
}

func (s *fakeSession) startLocked() {
	s.running = true
	s.interrupted = false
	s.notify("start")
}

func (s *fakeSession) notify(call string) {
	if s.calls != nil {
		s.calls <- call
	}
}

func (s *fakeSession) Interrupt() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupted = s.running
	s.permissions = nil
	s.notify("interrupt")
	return s.takeLocked()
}

func (s *fakeSession) TakeQueued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeLocked()
}

func (s *fakeSession) takeLocked() [][]agent.ContentPart {
	queue := s.queue
	s.queue = nil
	return queue
}

func (s *fakeSession) Queued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queue)
}

func (s *fakeSession) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *fakeSession) Permissions() []*session.PermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.permissions)
}

func (s *fakeSession) ResolvePermission(decision session.PermissionDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, decision)
	count := min(1, len(s.permissions))
	if decision == session.PermissionAllowSession {
		count = len(s.permissions)
	}
	s.permissions = slices.Delete(s.permissions, 0, count)
}

func (s *fakeSession) Sync(entries []session.Entry) ([]session.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript.Sync(entries)
}

func (s *fakeSession) Append(kind session.EntryKind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.Append(kind, text)
}

func (s *fakeSession) SetDraft(session.Draft) {}

func (s *fakeSession) SaveDraft(draft session.Draft) error {
	if s.save == nil {
		return nil
	}
	return s.save(draft)
}

func (s *fakeSession) Remember(prompt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remembered = append(s.remembered, prompt)
	return nil
}

func (s *fakeSession) Sessions() ([]session.Summary, error) {
	return s.summaries, nil
}

func (s *fakeSession) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if s.usage == nil {
		return backend.AccountUsage{}, nil
	}
	return s.usage(ctx)
}

func (s *fakeSession) request(requests ...*session.PermissionRequest) session.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions = append(s.permissions, requests...)
	return session.Update{Kind: session.UpdatePermissions}
}

func (s *fakeSession) event(event agent.Event) session.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.Apply(event)
	if event.Kind == agent.EventSteering {
		s.queue = slices.Delete(s.queue, 0, 1)
	}
	return session.Update{Kind: session.UpdateEvent, Event: event}
}

func (s *fakeSession) settle(err error) session.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.FinishTurn(s.interrupted, err)
	if s.interrupted {
		err = nil
	}
	s.running, s.interrupted = false, false
	return session.Update{Kind: session.UpdateSettled, Err: err}
}

func sessionEvent(update session.Update) tuiEvent {
	return tuiEvent{kind: tuiEventSession, update: update}
}

// newTestModel shows fake, or an empty session when fake is nil.
func newTestModel(width, height int, fake *fakeSession) *tuiModel {
	if fake == nil {
		fake = newFakeSession()
	}
	if fake.updates == nil {
		fake.updates = make(chan session.Update, 64)
	}
	if fake.settings.ThinkingLevel == "" {
		fake.settings.ThinkingLevel = agent.DefaultThinkingLevel
	}
	return newTUIModel(width, height, fake)
}

func testSession(model *tuiModel) *fakeSession {
	return model.session.(*fakeSession)
}

func setRunning(model *tuiModel) {
	model.running = true
	testSession(model).running = true
}

func setQueue(model *tuiModel, queue ...[]agent.ContentPart) {
	testSession(model).queue = queue
	model.syncQueue()
}

func onSave(model *tuiModel, save func(session.Draft) error) {
	testSession(model).save = save
}

func applyEvent(model *tuiModel, event agent.Event) {
	testSession(model).event(event)
	model.applyAgentEvent(event)
}

func streamAssistantText(model *tuiModel, text string) {
	testSession(model).event(agent.Event{Kind: agent.EventAssistantText, Text: text})
	model.syncConversation()
}

func testTextContent(text string) []agent.ContentPart {
	return []agent.ContentPart{{Kind: agent.ContentPartText, Text: text}}
}

func contentTexts(messages [][]agent.ContentPart) []string {
	texts := make([]string, len(messages))
	for index, content := range messages {
		texts[index] = contentText(content)
	}
	return texts
}

func TestNewRunnerRequiresTerminal(t *testing.T) {
	var output bytes.Buffer
	if _, err := NewRunner(strings.NewReader("/exit\n"), &output); !errors.Is(err, errNotTerminal) {
		t.Fatalf("NewRunner() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
