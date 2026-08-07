package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yaah/agent"
)

func TestSubagentDefinitionRequiresExplicitRequest(t *testing.T) {
	definition := NewSubagent(nil).Definition()
	if definition.Name != "subagent" || !strings.Contains(definition.Description, "only when the user explicitly asks") {
		t.Fatalf("definition = %+v", definition)
	}
	if definition.Parameters.Properties["tasks"].Items == nil || definition.Parameters.Properties["tasks"].Items.Type != "string" {
		t.Fatalf("tasks schema = %+v", definition.Parameters.Properties["tasks"])
	}
}

func TestSubagentRunsOneTask(t *testing.T) {
	subagent := NewSubagent(func(_ context.Context, task string, _ func(agent.Usage)) (agent.RunResult, error) {
		return agent.RunResult{Text: "result for " + task}, nil
	})

	result, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil)
	if err != nil || result.IsError || result.Output != "Subagent 1:\nresult for inspect" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestSubagentRunsConcurrentlyAndReturnsInputOrder(t *testing.T) {
	started := make(chan string, 2)
	releases := map[string]chan struct{}{
		"first":  make(chan struct{}),
		"second": make(chan struct{}),
	}
	secondFinished := make(chan struct{})
	subagent := NewSubagent(func(_ context.Context, task string, _ func(agent.Usage)) (agent.RunResult, error) {
		started <- task
		<-releases[task]
		if task == "second" {
			close(secondFinished)
		}
		return agent.RunResult{Text: task + " result"}, nil
	})

	done := make(chan struct {
		result agent.ToolResult
		err    error
	}, 1)
	go func() {
		result, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["first","second"]}`), nil)
		done <- struct {
			result agent.ToolResult
			err    error
		}{result: result, err: err}
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case task := <-started:
			seen[task] = true
		case <-time.After(2 * time.Second):
			t.Fatal("tasks did not start concurrently")
		}
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("started tasks = %v", seen)
	}

	close(releases["second"])
	select {
	case <-secondFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("second task did not finish")
	}
	close(releases["first"])

	result := <-done
	if result.err != nil || result.result.IsError {
		t.Fatalf("result = %+v, error = %v", result.result, result.err)
	}
	first := strings.Index(result.result.Output, "first result")
	second := strings.Index(result.result.Output, "second result")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("output order = %q", result.result.Output)
	}
}

func TestSubagentPublishesOutOfOrderLiveStatuses(t *testing.T) {
	started := make(chan string, 2)
	releases := map[string]chan struct{}{
		"first":  make(chan struct{}),
		"second": make(chan struct{}),
	}
	subagent := NewSubagent(func(_ context.Context, task string, _ func(agent.Usage)) (agent.RunResult, error) {
		started <- task
		<-releases[task]
		tokens := int64(100)
		if task == "second" {
			tokens = 200
		}
		return agent.RunResult{Text: task + " result", Usage: agent.Usage{TotalTokens: tokens}}, nil
	})
	updates := make(chan agent.ToolPresentation, 8)
	done := make(chan struct {
		result agent.ToolResult
		err    error
	}, 1)
	go func() {
		result, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["first","second"]}`), toolUpdateSinkFunc(func(presentation agent.ToolPresentation) error {
			updates <- presentation
			return nil
		}))
		done <- struct {
			result agent.ToolResult
			err    error
		}{result: result, err: err}
	}()

	initial := <-updates
	if initial.Title != "subagent" || initial.Arguments != "(2)" || !initial.Markdown || len(initial.Lines) != 2 || !strings.Contains(initial.Lines[0], "running (0s)") || !strings.Contains(initial.Lines[1], "running (0s)") {
		t.Fatalf("initial update = %+v", initial)
	}
	for range 2 {
		<-started
	}
	close(releases["second"])
	secondDone := <-updates
	if !strings.Contains(secondDone.Lines[0], "running") || !strings.Contains(secondDone.Lines[1], "complete") || !strings.Contains(secondDone.Lines[1], "200 tokens") {
		t.Fatalf("second completion update = %+v", secondDone)
	}
	close(releases["first"])
	final := <-updates
	if !strings.Contains(final.Lines[0], "complete") || !strings.Contains(final.Lines[1], "complete") {
		t.Fatalf("final update = %+v", final)
	}
	result := <-done
	if result.err != nil || result.result.IsError || strings.Index(result.result.Output, "first result") > strings.Index(result.result.Output, "second result") {
		t.Fatalf("result=%+v error=%v", result.result, result.err)
	}
}

func TestSubagentPublishesTokenUsageWhileRunning(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	subagent := NewSubagent(func(_ context.Context, _ string, usage func(agent.Usage)) (agent.RunResult, error) {
		usage(agent.Usage{TotalTokens: 321})
		close(started)
		<-release
		return agent.RunResult{Text: "done", Usage: agent.Usage{TotalTokens: 321}}, nil
	})
	updates := make(chan agent.ToolPresentation, 4)
	done := make(chan error, 1)
	go func() {
		_, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), toolUpdateSinkFunc(func(presentation agent.ToolPresentation) error {
			updates <- presentation
			return nil
		}))
		done <- err
	}()

	<-updates
	<-started
	select {
	case update := <-updates:
		if !strings.Contains(update.Lines[0], "running") || !strings.Contains(update.Lines[0], "321 tokens") {
			t.Fatalf("live usage update = %+v", update)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live usage update was not published")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestSubagentPresentationShowsElapsedTimeAndTokenCount(t *testing.T) {
	started := time.Unix(100, 0)
	presentation := subagentPresentation(
		[]string{"still working", "finished"},
		[]subagentStatus{
			{state: "running", started: started},
			{state: "complete", elapsed: 2*time.Second + 900*time.Millisecond, tokens: 1234},
		},
		started.Add(time.Minute+5*time.Second),
	)

	if !strings.Contains(presentation.Lines[0], "running (1m5s)") || !strings.Contains(presentation.Lines[1], "complete (2s, 1234 tokens)") {
		t.Fatalf("presentation = %+v", presentation)
	}
}

func TestSubagentUpdateFailureCancelsRemainingChildren(t *testing.T) {
	updateErr := errors.New("update failed")
	canceled := make(chan struct{})
	subagent := NewSubagent(func(ctx context.Context, task string, _ func(agent.Usage)) (agent.RunResult, error) {
		if task == "first" {
			return agent.RunResult{Text: "done"}, nil
		}
		<-ctx.Done()
		close(canceled)
		return agent.RunResult{}, ctx.Err()
	})
	updates := 0
	_, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["first","second"]}`), toolUpdateSinkFunc(func(agent.ToolPresentation) error {
		updates++
		if updates > 1 {
			return updateErr
		}
		return nil
	}))
	if !errors.Is(err, updateErr) {
		t.Fatalf("Execute() error = %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("remaining child was not canceled")
	}
}

func TestSubagentWaitsForMixedResults(t *testing.T) {
	failure := errors.New("child failed")
	subagent := NewSubagent(func(_ context.Context, task string, _ func(agent.Usage)) (agent.RunResult, error) {
		if task == "bad" {
			return agent.RunResult{}, failure
		}
		return agent.RunResult{Text: "useful finding"}, nil
	})

	result, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["good","bad"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "useful finding") || !strings.Contains(result.Output, failure.Error()) {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestSubagentValidatesAllTasksBeforeLaunching(t *testing.T) {
	var calls atomic.Int64
	subagent := NewSubagent(func(context.Context, string, func(agent.Usage)) (agent.RunResult, error) {
		calls.Add(1)
		return agent.RunResult{}, nil
	})

	for _, arguments := range []string{
		`{"tasks":[]}`,
		`{"tasks":["one","two","three","four","five"]}`,
		`{"tasks":["one","  "]}`,
	} {
		result, err := subagent.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("callback calls = %d", calls.Load())
	}
}

func TestSubagentPropagatesCancellationToAllTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	var finished sync.WaitGroup
	finished.Add(2)
	subagent := NewSubagent(func(ctx context.Context, _ string, _ func(agent.Usage)) (agent.RunResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		finished.Done()
		return agent.RunResult{}, ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		_, err := subagent.Execute(ctx, json.RawMessage(`{"tasks":["one","two"]}`), nil)
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("task did not start")
		}
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute() did not stop")
	}
	finished.Wait()
}

func TestSubagentBoundsCombinedOutput(t *testing.T) {
	subagent := NewSubagent(func(context.Context, string, func(agent.Usage)) (agent.RunResult, error) {
		return agent.RunResult{Text: strings.Repeat("x", defaultMaxBytes)}, nil
	})

	result, err := subagent.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two"]}`), nil)
	if err != nil || len(result.Output) > defaultMaxBytes || !strings.Contains(result.Output, "subagent output truncated") {
		t.Fatalf("output bytes = %d, error = %v", len(result.Output), err)
	}
}
