package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
		Input: strings.NewReader("/exit\n"), Output: &output, ErrorOutput: &output,
	})
	if !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunOneShotRendersEventsOnce(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		events := []agent.Event{
			{Kind: agent.EventAssistantReasoning, Text: "Assessing change"},
			{Kind: agent.EventAssistantText, Text: "Checking"},
			{Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "write-1", Name: "write"}, Presentation: agent.ToolPresentation{Title: "write", Arguments: "file.txt"}},
			{Kind: agent.EventToolUpdate, Call: agent.ToolCall{ID: "write-1", Name: "write"}, Presentation: agent.ToolPresentation{Title: "write", Arguments: "file.txt", Lines: []string{"preview must stay hidden"}}},
			{Kind: agent.EventToolExecute, Call: agent.ToolCall{ID: "write-1", Name: "write"}, Presentation: agent.ToolPresentation{Title: "write", Arguments: "file.txt"}},
			{Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "write-1", Name: "write"}, Presentation: agent.ToolPresentation{Title: "write", Arguments: "file.txt"}, Result: agent.ToolResult{Tool: "write", IsError: true, Output: "write failed"}},
			{Kind: agent.EventCompactionStart},
			{Kind: agent.EventCompactionEnd},
			{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 42}},
			{Kind: agent.EventGoalContinuation},
			{Kind: agent.EventAssistantText, Text: "Done"},
		}
		for _, event := range events {
			if err := sink(event); err != nil {
				return agent.RunResult{}, err
			}
		}
		return agent.RunResult{Text: "Done"}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "prompt", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Checking\nDone\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Assessing change\n") || !strings.Contains(stderr.String(), "[tool] write file.txt") || strings.Contains(stderr.String(), "content") || strings.Contains(stderr.String(), "preview must stay hidden") || !strings.Contains(stderr.String(), "write file.txt — error") || !strings.Contains(stderr.String(), "[context] compacting conversation") || !strings.Contains(stderr.String(), "[goal] continuing") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunOneShotIgnoresAbandonedStreamedTool(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventAssistantReasoning, Text: "reasoning"}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{
			Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "write-1", Name: "write"},
			Presentation: agent.ToolPresentation{Title: "write partial.txt"},
		}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{
			Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "write-1", Name: "write"},
			Presentation: agent.ToolPresentation{Title: "write partial.txt"},
			Result:       agent.ToolResult{IsError: true, Output: "tool call did not complete"},
		}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "prompt", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "reasoning\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRenderedOutputSanitizesControlsAndTruncatesDiagnostics(t *testing.T) {
	value := strings.Repeat("long-value", 30) + `"quoted`
	arguments, err := json.Marshal(map[string]string{"command": "printf " + value})
	if err != nil {
		t.Fatal(err)
	}

	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: "safe\x1b[31m\rrewrite\a"}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventToolExecute, Call: agent.ToolCall{Name: "bash", Arguments: arguments}, Presentation: agent.ToolPresentation{Title: "bash", Arguments: value}}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "prompt", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stdout.String()+stderr.String(), "\x1b\r\a") || !strings.Contains(stderr.String(), "...") {
		t.Fatalf("unsafe output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunOneShotSummarizesBashExit(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventToolExecute, Call: agent.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}, Presentation: agent.ToolPresentation{Title: "bash", Arguments: `"go test ./..."`}}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventToolEnd, Call: agent.ToolCall{Name: "bash"}, Presentation: agent.ToolPresentation{Title: "bash", Arguments: `"go test ./..."`, Outcome: "exit status: 1"}, Result: agent.ToolResult{Tool: "bash", IsError: true, Output: "failed\n[exit status: 1]"}}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "test", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), `[tool] bash "go test ./..."`) || !strings.Contains(stderr.String(), `bash "go test ./..." — exit status: 1`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunOneShotInterruptWaitsWithoutResettingConversation(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeEngine{}
	engine.runFunction = func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
		close(started)
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	}
	interrupts := make(chan os.Signal, 1)
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- RunOneShot(context.Background(), engine, "wait", Options{
			Output: &stdout, ErrorOutput: &stderr, Interrupts: interrupts,
		})
	}()
	<-started
	interrupts <- os.Interrupt

	select {
	case err := <-done:
		_, resets := engine.snapshot()
		if !errors.Is(err, ErrInterrupted) || resets != 0 {
			t.Fatalf("RunOneShot() error = %v, resets = %d", err, resets)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunOneShot() did not return")
	}
}

func TestRunOneShotReturnsParentCancellationWithoutResettingConversation(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeEngine{}
	engine.runFunction = func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
		close(started)
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- RunOneShot(ctx, engine, "wait", Options{Output: &stdout, ErrorOutput: &stderr})
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		_, resets := engine.snapshot()
		if !errors.Is(err, context.Canceled) || resets != 0 {
			t.Fatalf("RunOneShot() error = %v, resets = %d", err, resets)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunOneShot() did not return")
	}
}

func TestRunOneShotPropagatesOutputErrors(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		return agent.RunResult{}, sink(agent.Event{Kind: agent.EventAssistantText, Text: "answer"})
	}}
	err := RunOneShot(context.Background(), engine, "prompt", Options{Output: failingWriter{}, ErrorOutput: io.Discard})
	if !errors.Is(err, errOutput) {
		t.Fatalf("RunOneShot() error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
