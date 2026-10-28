package terminal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
)

type fakeEngine struct {
	mu                 sync.Mutex
	calls              []string
	compactions        int
	setGoalErr         error
	goal               *agent.GoalState
	runFunction        func(context.Context, string, agent.EventSink) (agent.RunResult, error)
	runContentFunction func(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error)
	compactFunction    func(context.Context, agent.EventSink) error
	steerFunction      func([]agent.ContentPart) bool
	clearFunction      func() [][]agent.ContentPart
}

func (e *fakeEngine) Run(ctx context.Context, prompt string, sink agent.EventSink) (agent.RunResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, prompt)
	function := e.runFunction
	e.mu.Unlock()

	if function == nil {
		return agent.RunResult{}, nil
	}
	return function(ctx, prompt, sink)
}

func (e *fakeEngine) RunContent(ctx context.Context, content []agent.ContentPart, sink agent.EventSink) (agent.RunResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, contentText(content))
	function := e.runContentFunction
	e.mu.Unlock()

	if function == nil {
		return agent.RunResult{}, nil
	}
	return function(ctx, content, sink)
}

func (e *fakeEngine) Compact(ctx context.Context, sink agent.EventSink) error {
	e.mu.Lock()
	e.compactions++
	function := e.compactFunction
	e.mu.Unlock()

	if function == nil {
		return nil
	}
	return function(ctx, sink)
}

func (e *fakeEngine) Steer(content []agent.ContentPart) bool {
	e.mu.Lock()
	function := e.steerFunction
	e.mu.Unlock()
	return function != nil && function(content)
}

func (e *fakeEngine) ClearSteering() [][]agent.ContentPart {
	e.mu.Lock()
	function := e.clearFunction
	e.mu.Unlock()
	if function == nil {
		return nil
	}
	return function()
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

func (e *fakeEngine) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *fakeEngine) compactionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compactions
}

type fakeEngineAPI interface {
	RunContent(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error)
	Compact(context.Context, agent.EventSink) error
	Steer([]agent.ContentPart) bool
	ClearSteering() [][]agent.ContentPart
	SetGoal(string) error
	Goal() (agent.GoalState, bool)
	ClearGoal()
}

func operationsFor(engine fakeEngineAPI) Operations {
	return Operations{
		RunTurn: func(ctx context.Context, content []agent.ContentPart, sink agent.EventSink) error {
			_, err := engine.RunContent(ctx, content, sink)
			return err
		},
		Compact: engine.Compact,
	}
}

func controlsFor(engine fakeEngineAPI) Controls {
	return Controls{
		Steer:         engine.Steer,
		ClearSteering: engine.ClearSteering,
		SetGoal:       engine.SetGoal,
		Goal:          engine.Goal,
		ClearGoal:     engine.ClearGoal,
	}
}

func optionsForEngine(engine fakeEngineAPI, options Options) Options {
	if options.Session == nil {
		options.Session = &testSession{}
	}
	options.Operations = operationsFor(engine)
	controls := controlsFor(engine)
	controls.SetThinkingLevel = options.Controls.SetThinkingLevel
	controls.SetFastMode = options.Controls.SetFastMode
	options.Controls = controls
	return options
}

type testSession struct {
	mu         sync.Mutex
	transcript session.Transcript
	save       func(session.Draft, bool) error
}

func (s *testSession) Sink(next agent.EventSink) agent.EventSink {
	return func(event agent.Event) error {
		s.mu.Lock()
		s.transcript.Apply(event)
		s.mu.Unlock()
		return next(event)
	}
}

func (s *testSession) Sync(entries []session.Entry) ([]session.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript.Sync(entries)
}

func (s *testSession) Append(kind session.EntryKind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.Append(kind, text)
}

func (s *testSession) AppendUser(content []agent.ContentPart) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.AppendUser(content)
}

func (s *testSession) FinishTurn(interrupted bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.FinishTurn(interrupted, err)
}

func (s *testSession) SetDraft(session.Draft) {}

func (s *testSession) SaveDraft(draft session.Draft, active bool) error {
	if s.save == nil {
		return nil
	}
	return s.save(draft, active)
}

func newTestModel(width, height int, options Options) *tuiModel {
	if options.Session == nil {
		options.Session = &testSession{}
	}
	return newTUIModel(width, height, options)
}

func onSave(model *tuiModel, save func(session.Draft, bool) error) {
	model.session.(*testSession).save = save
}

func applyEvent(model *tuiModel, event agent.Event) {
	_ = model.session.Sink(func(agent.Event) error { return nil })(event)
	model.applyAgentEvent(event)
}

func streamAssistantText(model *tuiModel, text string) {
	_ = model.session.Sink(func(agent.Event) error { return nil })(agent.Event{Kind: agent.EventAssistantText, Text: text})
	model.syncConversation()
}

func TestNewRunnerRequiresTerminal(t *testing.T) {
	var output bytes.Buffer
	if _, err := NewRunner(strings.NewReader("/exit\n"), &output); !errors.Is(err, errNotTerminal) {
		t.Fatalf("NewRunner() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
