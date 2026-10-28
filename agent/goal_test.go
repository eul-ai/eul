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
	goal := &Goal{}
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{Text: "partial", State: []byte("partial-state")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "partial-state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("goal continuation request = %+v", request)
			}
			if !strings.Contains(request.Inputs[0].PlainText(), "finish the migration") {
				t.Fatalf("goal continuation input = %+v", request.Inputs[0])
			}
			goal.Clear()
			return Response{Text: "done", State: []byte("done-state")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("finish the migration"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	result, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
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
	goal := &Goal{}
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
			goal.Clear()
			return Response{Text: "redirected"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-started
	if !engine.Steer(textParts("user redirect")) {
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
	goal := &Goal{}
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
			if string(request.State) != "steer-two" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || !strings.Contains(request.Inputs[0].PlainText(), "keep going") {
				t.Fatalf("goal request = %+v", request)
			}
			goal.Clear()
			return Response{Text: "done"}, nil
		},
	}}
	engine = newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-started
	if !engine.Steer(textParts("steer one")) || !engine.Steer(textParts("steer two")) {
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
	goal := &Goal{}
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
			goal.Clear()
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{definitions: toolDefinitions("tool"), execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "tool result"}, nil
	}}, Options{Goal: goal})
	if err := goal.Set("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	if _, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
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
	goal := &Goal{}
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
	engine := newTestEngine(t, provider, &fakeToolbox{definitions: toolDefinitions("tool"), execute: func(context.Context, ToolCall) (ToolResult, error) {
		executions <- struct{}{}
		startedOnce.Do(func() { close(toolStarted) })
		<-releaseTool
		return ToolResult{Output: "result"}, nil
	}}, Options{Goal: goal})
	if err := goal.Set("keep going"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()
	<-toolStarted
	goal.Clear()
	close(releaseTool)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 || provider.calls != 2 || slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("executions=%d calls=%d events=%v", len(executions), provider.calls, eventKinds(events))
	}
}

func TestEngineCompletionToolStopsGoalContinuation(t *testing.T) {
	goal := &Goal{}
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
	toolbox := &fakeToolbox{definitions: toolDefinitions("update_goal"), execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		if call.Name != "update_goal" {
			t.Fatalf("tool = %q", call.Name)
		}
		return ToolResult{Output: "Goal marked complete."}, goal.Complete()
	}}
	engine := newTestEngine(t, provider, toolbox, Options{Goal: goal})
	if err := goal.Set("finish"); err != nil {
		t.Fatal(err)
	}

	var events []Event
	result, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Text != "complete" || provider.calls != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, provider.calls, err)
	}
	state, ok := goal.State()
	if !ok || !state.Complete || slices.Contains(eventKinds(events), EventGoalContinuation) {
		t.Fatalf("goal=%+v exists=%v events=%v", goal, ok, eventKinds(events))
	}
}

func TestEngineRollsBackGoalInputWhenEventFails(t *testing.T) {
	goal := &Goal{}
	sinkErr := errors.New("sink failed")
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
			goal.Clear()
			return Response{Text: "recovered"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Run(context.Background(), textParts("first"), func(event Event) error {
		if event.Kind == EventGoalContinuation {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("first run error = %v", err)
	}
	result, err := engine.Run(context.Background(), textParts("recover"), discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("recovery result=%+v error=%v", result, err)
	}
}

func TestEngineClearGoalDuringGenerationPreventsContinuation(t *testing.T) {
	goal := &Goal{}
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
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), textParts("start"), discardEvents)
		done <- err
	}()
	<-started
	goal.Clear()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
}

func TestGoalRejectsInactiveAndRepeatedCompletion(t *testing.T) {
	goal := &Goal{}
	if err := goal.Complete(); !errors.Is(err, ErrNoGoal) {
		t.Fatalf("inactive completion error = %v", err)
	}
	if err := goal.Set("finish"); err != nil {
		t.Fatal(err)
	}
	if err := goal.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := goal.Complete(); !errors.Is(err, ErrGoalAlreadyComplete) {
		t.Fatalf("repeated completion error = %v", err)
	}
}

func TestGoalReplacesObjective(t *testing.T) {
	goal := &Goal{}
	if err := goal.Set("first"); err != nil {
		t.Fatal(err)
	}
	if err := goal.Set("second"); err != nil {
		t.Fatal(err)
	}
	state, ok := goal.State()
	if !ok || state.Objective != "second" || state.Complete {
		t.Fatalf("replacement goal = %+v, exists=%v", state, ok)
	}
}

func TestEngineToolLifecycleFailureRetainsGoalWithoutContinuing(t *testing.T) {
	goal := &Goal{}
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
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
		if event.Kind == EventToolExecute {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) || provider.calls != 1 {
		t.Fatalf("run error=%v calls=%d", err, provider.calls)
	}
	state, ok := goal.State()
	if !ok || state.Objective != "continue" || state.Complete {
		t.Fatalf("goal after tool lifecycle failure = %+v, exists=%v", state, ok)
	}
}

func TestEngineFailureRetainsGoal(t *testing.T) {
	goal := &Goal{}
	failure := errors.New("provider failed")
	provider := streamingProviderFunc(func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
		return Response{}, failure
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Run(context.Background(), textParts("start"), discardEvents); !errors.Is(err, failure) {
		t.Fatalf("run error = %v", err)
	}
	state, ok := goal.State()
	if !ok || state.Objective != "continue" || state.Complete {
		t.Fatalf("goal after failure = %+v, exists=%v", state, ok)
	}
}

func TestEngineCancellationRetainsGoal(t *testing.T) {
	goal := &Goal{}
	started := make(chan struct{})
	provider := streamingProviderFunc(func(ctx context.Context, _ Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		close(started)
		<-ctx.Done()
		return Response{}, ctx.Err()
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, textParts("start"), discardEvents)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
	state, ok := goal.State()
	if !ok || state.Objective != "continue" || state.Complete {
		t.Fatalf("goal after cancellation = %+v, exists=%v", state, ok)
	}
}
