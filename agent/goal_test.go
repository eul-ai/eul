package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestEngineContinuesActiveGoalBeforeSettling(t *testing.T) {
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{Text: "partial", State: []byte("partial-state")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "partial-state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("goal continuation request = %+v", request)
			}
			prompt := request.Inputs[0].Text
			if !strings.Contains(prompt, "finish the migration") || !strings.Contains(prompt, "update_goal") {
				t.Fatalf("goal continuation prompt = %q", prompt)
			}
			engine.ClearGoal()
			return Response{Text: "done", State: []byte("done-state")}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("finish the migration"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	result, err := engine.Run(context.Background(), "start", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Text != "done" || provider.calls != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, provider.calls, err)
	}
	if got := eventKinds(events); !slices.Contains(got, EventGoalContinuation) {
		t.Fatalf("events = %v", got)
	}
}

func TestEngineGivesSteeringPriorityOverGoal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "initial", State: []byte("initial")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "user redirect")
			if string(request.State) != "initial" {
				t.Fatalf("steering state = %q", request.State)
			}
			engine.ClearGoal()
			return Response{Text: "redirected"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-started
	if !engine.Steer("user redirect") {
		t.Fatal("engine rejected steering")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("goal continued before steering: %v", eventKinds(events))
	}
}

func TestEngineDrainsSteeringBeforeGoalContinuation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "initial", State: []byte("initial")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "steer one")
			return Response{Text: "first steering", State: []byte("steer-one")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "steer two")
			return Response{Text: "second steering", State: []byte("steer-two")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "steer-two" || len(request.Inputs) != 1 || !strings.Contains(request.Inputs[0].Text, "keep going") {
				t.Fatalf("goal request = %+v", request)
			}
			engine.ClearGoal()
			return Response{Text: "done"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-started
	if !engine.Steer("steer one") || !engine.Steer("steer two") {
		t.Fatal("engine rejected steering")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	var continuations []EventKind
	for _, event := range events {
		if event.Kind == EventSteering || event.Kind == EventGoalContinuation {
			continuations = append(continuations, event.Kind)
		}
	}
	want := []EventKind{EventSteering, EventSteering, EventGoalContinuation}
	if !slices.Equal(continuations, want) {
		t.Fatalf("continuations = %v, want %v", continuations, want)
	}
}

func TestEngineDoesNotInjectGoalAfterToolBatch(t *testing.T) {
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				ToolCalls: []ToolCall{{ID: "tool-call", Name: "tool", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tool-state"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "tool-state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult {
				t.Fatalf("tool continuation request = %+v", request)
			}
			engine.ClearGoal()
			return Response{Text: "done"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "tool result"}, nil
	}}, Options{})
	if err := engine.SetGoal("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	if _, err := engine.Run(context.Background(), "start", func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("goal was injected after tool batch: %v", eventKinds(events))
	}
}

func TestEngineClearGoalDuringToolBatchFinishesResultsAndStopsContinuation(t *testing.T) {
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				ToolCalls: []ToolCall{
					{ID: "one", Name: "tool", Arguments: json.RawMessage(`{}`)},
					{ID: "two", Name: "tool", Arguments: json.RawMessage(`{}`)},
				},
				State: []byte("tool-state"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "tool-state" || len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[1].Kind != InputToolResult {
				t.Fatalf("tool continuation request = %+v", request)
			}
			return Response{Text: "stopped"}, nil
		},
	}}
	executions := make(chan struct{}, 2)
	var startedOnce sync.Once
	engine := newTestEngine(t, provider, &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions <- struct{}{}
		startedOnce.Do(func() { close(toolStarted) })
		<-releaseTool
		return ToolResult{Output: "result " + call.ID}, nil
	}}, Options{})
	if err := engine.SetGoal("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-toolStarted
	engine.ClearGoal()
	close(releaseTool)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 || provider.calls != 2 || slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("executions=%d calls=%d events=%v", len(executions), provider.calls, eventKinds(events))
	}
}

func TestEngineCompletionToolStopsGoalContinuation(t *testing.T) {
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				ToolCalls: []ToolCall{{ID: "complete", Name: "update_goal", Arguments: json.RawMessage(`{"status":"complete"}`)}},
				State:     []byte("completion-state"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "completion-state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult {
				t.Fatalf("completion continuation request = %+v", request)
			}
			return Response{Text: "complete"}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		if call.Name != "update_goal" {
			t.Fatalf("tool = %q", call.Name)
		}
		return ToolResult{Output: "Goal marked complete."}, engine.CompleteGoal()
	}}
	engine = newTestEngine(t, provider, toolbox, Options{})
	if err := engine.SetGoal("finish"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	result, err := engine.Run(context.Background(), "start", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Text != "complete" || provider.calls != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, provider.calls, err)
	}
	goal, ok := engine.Goal()
	if !ok || !goal.Complete || slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("goal=%+v exists=%v events=%v", goal, ok, eventKinds(events))
	}
}

func TestEngineRollsBackGoalInputWhenEventFails(t *testing.T) {
	sinkErr := errors.New("sink failed")
	var engine *Engine
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "first")
			return Response{Text: "partial", State: []byte("partial-state")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "recover")
			if string(request.State) != "partial-state" {
				t.Fatalf("recovery state = %q", request.State)
			}
			engine.ClearGoal()
			return Response{Text: "recovered"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Run(context.Background(), "first", func(event Event) error {
		if event.Kind == EventGoalContinuation {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("first run error = %v", err)
	}
	result, err := engine.Run(context.Background(), "recover", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("recovery result=%+v error=%v", result, err)
	}
}

func TestEngineClearGoalDuringGenerationPreventsContinuation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "stopped"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", discardEvents)
		done <- err
	}()
	<-started
	engine.ClearGoal()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
}

func TestEngineRejectsInactiveAndRepeatedGoalCompletion(t *testing.T) {
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	if err := engine.CompleteGoal(); err == nil || !strings.Contains(err.Error(), "no goal") {
		t.Fatalf("inactive completion error = %v", err)
	}
	if err := engine.SetGoal("finish"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CompleteGoal(); err != nil {
		t.Fatal(err)
	}
	if err := engine.CompleteGoal(); err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Fatalf("repeated completion error = %v", err)
	}
}

func TestEngineReplacesGoal(t *testing.T) {
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("first"); err != nil {
		t.Fatal(err)
	}
	if err := engine.SetGoal("second"); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "second" || goal.Complete {
		t.Fatalf("replacement goal = %+v, exists=%v", goal, ok)
	}
}

func TestEngineToolLifecycleFailureRetainsGoalWithoutContinuing(t *testing.T) {
	sinkErr := errors.New("sink failed")
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				ToolCalls: []ToolCall{{ID: "tool", Name: "tool", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tool-state"),
			}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventToolExecute {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) || provider.calls != 1 {
		t.Fatalf("run error=%v calls=%d", err, provider.calls)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "continue" || goal.Complete {
		t.Fatalf("goal after tool lifecycle failure = %+v, exists=%v", goal, ok)
	}
}

func TestEngineFailureRetainsGoal(t *testing.T) {
	failure := errors.New("provider failed")
	provider := streamingProviderFunc(func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
		return Response{}, failure
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Run(context.Background(), "start", discardEvents); !errors.Is(err, failure) {
		t.Fatalf("run error = %v", err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "continue" || goal.Complete {
		t.Fatalf("goal after failure = %+v, exists=%v", goal, ok)
	}
}

func TestEngineCancellationRetainsGoal(t *testing.T) {
	started := make(chan struct{})
	provider := streamingProviderFunc(func(ctx context.Context, _ Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		close(started)
		<-ctx.Done()
		return Response{}, ctx.Err()
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, "start", discardEvents)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "continue" || goal.Complete {
		t.Fatalf("goal after cancellation = %+v, exists=%v", goal, ok)
	}
}

func TestEngineResetClearsGoal(t *testing.T) {
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Reset(); err != nil {
		t.Fatal(err)
	}
	if goal, ok := engine.Goal(); ok {
		t.Fatalf("goal after reset = %+v", goal)
	}
}
