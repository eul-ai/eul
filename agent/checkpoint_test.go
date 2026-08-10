package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestEngineCheckpointRoundTrip(t *testing.T) {
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	engine.state = []byte("provider-state")
	engine.contextUsage = Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	engine.pendingInputs = []Input{
		{Kind: InputUser, Text: "pending user"},
		{Kind: InputToolResult, Text: "result", CallID: "call", Tool: "read"},
	}
	if err := engine.SetGoal("finish migration"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CompleteGoal(); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := engine.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	engine.state[0] = 'X'
	engine.pendingInputs[0].Text = "changed"

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	if err := restored.RestoreCheckpoint(decoded); err != nil {
		t.Fatal(err)
	}
	if string(restored.state) != "provider-state" {
		t.Fatalf("state = %q", restored.state)
	}
	wantInputs := []Input{
		{Kind: InputUser, Text: "pending user"},
		{Kind: InputToolResult, Text: "result", CallID: "call", Tool: "read"},
	}
	if !slices.Equal(restored.pendingInputs, wantInputs) || restored.contextUsage.TotalTokens != 12 {
		t.Fatalf("inputs=%+v usage=%+v", restored.pendingInputs, restored.contextUsage)
	}
	goal, ok := restored.Goal()
	if !ok || goal.Objective != "finish migration" || !goal.Complete {
		t.Fatalf("goal=%+v exists=%v", goal, ok)
	}
}

func TestEngineCheckpointEventsMarkResumableBoundaries(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				State:     []byte("tools"),
				ToolCalls: []ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}},
				Usage:     Usage{TotalTokens: 10},
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "tools" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult {
				t.Fatalf("continuation request = %+v", request)
			}
			return Response{Text: "done", State: []byte("done"), Usage: Usage{TotalTokens: 12}}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "contents"}, nil
	}}, Options{Checkpointing: true})

	var events []Event
	if _, err := engine.Run(context.Background(), "start", func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{
		EventContextUsage,
		EventToolStart,
		EventToolExecute,
		EventToolEnd,
		EventCheckpoint,
		EventContextUsage,
		EventCheckpoint,
	}
	if got := eventKinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("events = %v, want %v", got, wantKinds)
	}
	for _, event := range events {
		if event.Kind == EventCheckpoint && event.Checkpoint == nil {
			t.Fatal("checkpoint event has no checkpoint")
		}
	}
}

func TestCheckpointFailureStopsGoalContinuation(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{Text: "partial", State: []byte("partial")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Checkpointing: true})
	if err := engine.SetGoal("continue"); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("persist failed")

	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventCheckpoint {
			return persistErr
		}
		return nil
	})
	if !errors.Is(err, persistErr) || provider.calls != 1 {
		t.Fatalf("error=%v provider calls=%d", err, provider.calls)
	}
}
