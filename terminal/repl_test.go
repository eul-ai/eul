package terminal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"yaah/agent"
)

type fakeEngine struct {
	mu            sync.Mutex
	calls         []string
	resets        int
	resetErr      error
	setGoalErr    error
	goal          *agent.GoalState
	runFunction   func(context.Context, string, agent.EventSink) (agent.RunResult, error)
	steerFunction func(string) bool
	clearFunction func() []string
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

func (e *fakeEngine) Steer(prompt string) bool {
	e.mu.Lock()
	function := e.steerFunction
	e.mu.Unlock()
	return function != nil && function(prompt)
}

func (e *fakeEngine) ClearSteering() []string {
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

func (e *fakeEngine) Reset() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resets++
	if e.resetErr == nil {
		e.goal = nil
	}
	return e.resetErr
}

func (e *fakeEngine) snapshot() ([]string, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...), e.resets
}

func TestRunRequiresTerminal(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), &fakeEngine{}, Options{
		Input: strings.NewReader("/exit\n"), Output: &output,
	})
	if !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Run() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
