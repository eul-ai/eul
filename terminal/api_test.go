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
	steerFunction      func(string) bool
	clearFunction      func() []string
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

func TestRunRequiresTerminal(t *testing.T) {
	var output bytes.Buffer
	_, err := Run(context.Background(), &fakeEngine{}, Options{
		Input: strings.NewReader("/exit\n"), Output: &output,
	})
	if !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunValidatesCheckpointCapabilityBeforeTerminalSetup(t *testing.T) {
	var output bytes.Buffer
	_, err := Run(context.Background(), &fakeEngine{}, Options{
		Input:       strings.NewReader(""),
		Output:      &output,
		Persistence: testCommands{saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error { return nil }},
	})
	if !errors.Is(err, errCheckpointUnavailable) {
		t.Fatalf("Run() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("terminal output = %q", output.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
