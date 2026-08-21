package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestEngineCheckpointRoundTrip(t *testing.T) {
	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	engine.conversation.state = []byte("provider-state")
	engine.conversation.usage = Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	engine.conversation.inputs = []Input{
		{Kind: InputUser, Content: []ContentPart{
			{Kind: ContentPartText, Text: "pending user"},
			{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
		}},
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
	engine.conversation.state[0] = 'X'
	engine.conversation.inputs[0].Content[0].Text = "changed"
	engine.conversation.inputs[0].Content[1].Image.Data[0] = 'X'

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
	goal, ok := restored.Goal()
	if !ok || goal.Objective != "finish migration" || !goal.Complete {
		t.Fatalf("goal=%+v exists=%v", goal, ok)
	}
}

func TestCheckpointRejectsContentOnToolResult(t *testing.T) {
	checkpoint := Checkpoint{data: checkpointData{
		Version: checkpointVersion,
		PendingInputs: []Input{{
			Kind:    InputToolResult,
			CallID:  "call",
			Tool:    "read",
			Content: []ContentPart{{Kind: ContentPartText, Text: "invalid"}},
		}},
	}}
	if _, err := json.Marshal(checkpoint); err == nil {
		t.Fatal("checkpoint accepted content on a tool result")
	}
}

func TestVersionThreeCheckpointFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/checkpoint-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(fixture, &checkpoint); err != nil {
		t.Fatal(err)
	}

	engine := newTestEngine(t, &scriptedProvider{t: t}, &fakeToolbox{}, Options{})
	if err := engine.RestoreCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	if string(engine.conversation.state) != "provider-state-v1" || engine.conversation.usage != (Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}) {
		t.Fatalf("conversation = %+v", engine.conversation)
	}
	if len(engine.conversation.inputs) != 2 || engine.conversation.inputs[0].Kind != InputUser || engine.conversation.inputs[1].Kind != InputToolResult || engine.conversation.inputs[1].CallID != "call-1" {
		t.Fatalf("inputs = %+v", engine.conversation.inputs)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "finish migration" || goal.Complete {
		t.Fatalf("goal = %+v, exists = %t", goal, ok)
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpointSemanticJSON(t, encoded, fixture)
}

func assertCheckpointSemanticJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\ngot:  %s\nwant: %s", got, want)
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
