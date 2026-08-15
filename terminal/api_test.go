package terminal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/eul-ai/eul/agent"
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
		RunTurn: func(ctx context.Context, content []agent.ContentPart, stream EventStream) error {
			_, err := engine.RunContent(ctx, content, stream.Emit)
			return err
		},
		Compact: func(ctx context.Context, stream EventStream) error {
			return engine.Compact(ctx, stream.Emit)
		},
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
	options.Operations = operationsFor(engine)
	controls := controlsFor(engine)
	controls.SetThinkingLevel = options.Controls.SetThinkingLevel
	controls.SetFastMode = options.Controls.SetFastMode
	options.Controls = controls
	return options
}

func TestRunRequiresTerminal(t *testing.T) {
	var output bytes.Buffer
	_, err := Run(context.Background(), optionsForEngine(&fakeEngine{}, Options{
		Input: strings.NewReader("/exit\n"), Output: &output,
	}))
	if !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Run() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
