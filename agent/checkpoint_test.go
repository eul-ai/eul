package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestEngineCheckpointRoundTrip(t *testing.T) {
	goal := &Goal{}
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{Goal: goal})
	engine.conversation.state = []byte("provider-state")
	engine.conversation.usage = Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	engine.conversation.inputs = []Input{
		{Kind: InputUser, Content: []ContentPart{
			{Kind: ContentPartText, Text: "pending user"},
			{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
		}},
		{Kind: InputToolResult, Text: "result", CallID: "call", Tool: "read"},
	}
	if err := goal.Set("finish migration"); err != nil {
		t.Fatal(err)
	}
	if err := goal.Complete(); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := engine.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	restoredGoal := &Goal{}
	restored := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{Goal: restoredGoal, Checkpoint: decoded})
	if string(restored.conversation.state) != "provider-state" {
		t.Fatalf("state = %q", restored.conversation.state)
	}
	wantInputs := []Input{
		{Kind: InputUser, Content: []ContentPart{
			{Kind: ContentPartText, Text: "pending user"},
			{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
		}},
		{Kind: InputToolResult, Text: "result", CallID: "call", Tool: "read"},
	}
	if !reflect.DeepEqual(restored.conversation.inputs, wantInputs) || restored.conversation.usage.TotalTokens != 12 {
		t.Fatalf("inputs=%+v usage=%+v", restored.conversation.inputs, restored.conversation.usage)
	}
	state, ok := restoredGoal.State()
	if !ok || state.Objective != "finish migration" || !state.Complete {
		t.Fatalf("goal=%+v exists=%v", state, ok)
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
	engine := newTestEngine(t, provider, &fakeToolbox{definitions: toolDefinitions("read"), execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "contents"}, nil
	}}, Options{Checkpointing: true})

	var events []Event
	if _, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
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
	goal := &Goal{}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Checkpointing: true, Goal: goal})
	if err := goal.Set("continue"); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("persist failed")

	_, err := engine.Run(context.Background(), textParts("start"), func(event Event) error {
		if event.Kind == EventCheckpoint {
			return persistErr
		}
		return nil
	})
	if !errors.Is(err, persistErr) || provider.calls != 1 {
		t.Fatalf("error=%v provider calls=%d", err, provider.calls)
	}
}
