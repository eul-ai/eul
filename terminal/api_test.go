package terminal

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
)

type fakeEngine struct {
	mu         sync.Mutex
	setGoalErr error
	goal       *agent.GoalState
}

func (e *fakeEngine) SetGoal(objective string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.setGoalErr != nil {
		return e.setGoalErr
	}
	e.goal = &agent.GoalState{Objective: objective}
	return nil
}

func (e *fakeEngine) Goal() (agent.GoalState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.goal == nil {
		return agent.GoalState{}, false
	}
	return *e.goal, true
}

func (e *fakeEngine) ClearGoal() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.goal = nil
}

func controlsFor(engine *fakeEngine) Controls {
	return Controls{
		SetGoal:   engine.SetGoal,
		Goal:      engine.Goal,
		ClearGoal: engine.ClearGoal,
	}
}

// fakeSession records what the terminal asks of a session. Tests play the
// session's side by sending the updates that event and settle return.
type fakeSession struct {
	mu          sync.Mutex
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
}

func newFakeSession() *fakeSession {
	return &fakeSession{updates: make(chan session.Update, 64)}
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

func newTestModel(width, height int, options Options) *tuiModel {
	if options.Session == nil {
		options.Session = newFakeSession()
	}
	return newTUIModel(width, height, options)
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
