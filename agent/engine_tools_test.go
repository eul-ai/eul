package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEngineUsesFallbackPresentationForExecutionOnlyToolbox(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(context.Context, Request, TextSink) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "call", Name: "read"}}}, nil
		},
		func(context.Context, Request, TextSink) (Response, error) {
			return Response{Text: "done"}, nil
		},
	}}
	toolbox := &executionOnlyToolbox{
		definitions: []ToolDefinition{{Name: "read"}},
		execute: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "contents"}, nil
		},
	}
	var presentations []ToolPresentation
	engine := newTestEngine(t, provider, toolbox, Options{})
	if _, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventToolStart {
			presentations = append(presentations, event.Presentation)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(presentations) != 1 || presentations[0].Title != "read" {
		t.Fatalf("presentations = %+v", presentations)
	}
}

func TestEngineRunsToolLoopAndCarriesProviderState(t *testing.T) {
	provider := &scriptedProvider{t: t, reasoning: "Assessing files"}
	provider.steps = []providerStep{
		func(_ context.Context, request Request, onText TextSink) (Response, error) {
			assertUserInput(t, request, "inspect the file")
			if len(request.State) != 0 {
				t.Fatalf("initial state = %q, want empty", request.State)
			}
			if got := toolNames(request.Tools); !slices.Equal(got, []string{"read", "write"}) {
				t.Fatalf("tool order = %v, want [read write]", got)
			}
			for _, definition := range request.Tools {
				if strings.Contains(request.Instructions, definition.Description) {
					t.Fatalf("instructions duplicate tool description %q:\n%s", definition.Description, request.Instructions)
				}
			}
			if err := onText("Checking"); err != nil {
				return Response{}, err
			}
			return Response{
				Text:      "Checking",
				ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)}},
				State:     []byte("state-1"),
				Usage:     Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			}, nil
		},
		func(_ context.Context, request Request, onText TextSink) (Response, error) {
			if string(request.State) != "state-1" {
				t.Fatalf("continuation state = %q, want state-1", request.State)
			}
			if len(request.Inputs) != 1 {
				t.Fatalf("tool result inputs = %d, want 1", len(request.Inputs))
			}
			input := request.Inputs[0]
			if input.Kind != InputToolResult || input.CallID != "call-1" || input.Tool != "read" || input.PlainText() != "file contents" || input.IsError {
				t.Fatalf("unexpected tool result input: %+v", input)
			}
			if err := onText(" done"); err != nil {
				return Response{}, err
			}
			return Response{
				Text:  "done",
				State: []byte("state-2"),
				Usage: Usage{InputTokens: 8, OutputTokens: 3, TotalTokens: 11},
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "next question")
			if string(request.State) != "state-2" {
				t.Fatalf("state on next user turn = %q, want state-2", request.State)
			}
			return Response{Text: "next answer", State: []byte("state-3")}, nil
		},
	}

	toolbox := &fakeToolbox{
		definitions: []ToolDefinition{
			{Name: "read", Description: "opaque-read-description"},
			{Name: "write", Description: "opaque-write-description"},
		},
		execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
			if call.ID != "call-1" || call.Name != "read" || string(call.Arguments) != `{"path":"README.md"}` {
				t.Fatalf("unexpected tool call: %+v", call)
			}
			return ToolResult{Output: "file contents"}, nil
		},
	}

	engine := newTestEngine(t, provider, toolbox, Options{Model: "test-model"})
	var events []Event
	result, err := engine.Run(context.Background(), "inspect the file", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Text != "done" {
		t.Fatalf("result text = %q, want %q", result.Text, "done")
	}
	if result.Usage != (Usage{InputTokens: 18, OutputTokens: 5, TotalTokens: 23}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
	wantKinds := []EventKind{EventAssistantReasoning, EventAssistantText, EventContextUsage, EventToolStart, EventToolExecute, EventToolEnd, EventAssistantText, EventContextUsage}
	if got := eventKinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", got, wantKinds)
	}
	if events[0].Text != "Assessing files" || events[1].Text != "Checking" || events[2].Usage.TotalTokens != 12 || events[3].Call.ID != "call-1" || events[4].Call.ID != "call-1" || events[5].Result.CallID != "call-1" || events[6].Text != " done" || events[7].Usage.TotalTokens != 11 {
		t.Fatalf("unexpected event payloads: %+v", events)
	}

	next, err := engine.Run(context.Background(), "next question", discardEvents)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	if next.Text != "next answer" {
		t.Fatalf("second result text = %q", next.Text)
	}
}

func TestCompleteToolCallSnapshotPreservesRawJSON(t *testing.T) {
	call := ToolCall{ID: "call-1", Name: "write", Arguments: json.RawMessage(`{"path":"demo.go"}`)}
	snapshot := completeToolCallSnapshot(call)
	if snapshot.ID != call.ID || snapshot.Name != call.Name || !snapshot.Complete || snapshot.RawArguments != string(call.Arguments) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestToolPresentationCloneAndEqual(t *testing.T) {
	presentation := ToolPresentation{
		Diff:      []ToolDiffLine{{Kind: ToolDiffLineAdded, NewLine: 2, Text: "new"}},
		TailLines: 5,
		Elapsed:   2 * time.Second,
	}
	cloned := presentation.Clone()
	if !presentation.Equal(cloned) {
		t.Fatalf("cloned presentation differs: original=%+v clone=%+v", presentation, cloned)
	}
	changed := cloned
	changed.TailLines++
	if presentation.Equal(changed) {
		t.Fatal("presentations with different tail limits compare equal")
	}
	changed = cloned
	changed.Elapsed++
	if presentation.Equal(changed) {
		t.Fatal("presentations with different elapsed times compare equal")
	}
	changed = cloned
	changed.LinesTruncated = !changed.LinesTruncated
	if presentation.Equal(changed) {
		t.Fatal("presentations with different truncation states compare equal")
	}

	cloned.Diff[0].Text = "changed"
	if presentation.Diff[0].Text != "new" {
		t.Fatalf("clone shares diff storage: original=%+v clone=%+v", presentation, cloned)
	}
	if presentation.Equal(cloned) {
		t.Fatalf("different presentations compare equal: original=%+v clone=%+v", presentation, cloned)
	}
}

func TestEngineUsesFinalPresentationOnlyOnToolEnd(t *testing.T) {
	providerCalls := 0
	provider := streamingProviderFunc(func(_ context.Context, _ Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		providerCalls++
		if providerCalls == 1 {
			return Response{ToolCalls: []ToolCall{{ID: "edit-1", Name: "edit", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		return Response{Text: "done"}, nil
	})
	final := ToolPresentation{
		Title: "edit", Arguments: "demo.go",
		Diff: []ToolDiffLine{{Kind: ToolDiffLineAdded, NewLine: 1, Text: "new"}},
	}
	toolbox := &fakeToolbox{executeWithUpdates: func(_ context.Context, _ ToolCall, updates ToolUpdateSink) (ToolResult, error) {
		updates.SetFinal(final)
		if err := updates.Update(ToolPresentation{Title: "edit", Lines: []string{"late update"}}); err != nil {
			t.Fatalf("late update error = %v", err)
		}
		final.Diff[0].Text = "mutated after finalization"
		return ToolResult{Output: "edited demo.go"}, nil
	}}
	var events []Event
	engine := newTestEngine(t, provider, toolbox, Options{})
	if _, err := engine.Run(context.Background(), "edit", func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, event := range events {
		if event.Kind == EventToolUpdate {
			t.Fatalf("final presentation emitted as a tool update: %+v", events)
		}
	}
	for _, event := range events {
		if event.Kind == EventToolEnd {
			if len(event.Presentation.Diff) != 1 || event.Presentation.Diff[0].Text != "new" {
				t.Fatalf("tool end presentation = %+v", event.Presentation)
			}
			return
		}
	}
	t.Fatalf("missing tool end event: %+v", events)
}

func TestEngineStreamsToolPresentationBeforeGenerationAndExecutionFinish(t *testing.T) {
	providerCalls := 0
	executed := false
	var events []Event
	provider := streamingProviderFunc(func(_ context.Context, _ Request, _ TextSink, _ TextSink, onToolCall ToolCallSink) (Response, error) {
		providerCalls++
		if providerCalls > 1 {
			return Response{Text: "done"}, nil
		}

		if err := onToolCall(ToolCallSnapshot{
			ID: "write-1", Name: "write", RawArguments: `{"path":"demo.go","content":"hel`,
		}); err != nil {
			return Response{}, err
		}
		if executed || len(events) == 0 || events[len(events)-1].Kind != EventToolStart {
			t.Fatalf("partial tool call was not delivered before execution: executed=%v events=%v", executed, eventKinds(events))
		}
		if err := onToolCall(ToolCallSnapshot{
			ID: "write-1", Name: "write", RawArguments: `{"path":"demo.go","content":"hello"}`, Complete: true,
		}); err != nil {
			return Response{}, err
		}
		return Response{ToolCalls: []ToolCall{{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{"path":"demo.go","content":"hello"}`)}}}, nil
	})
	toolbox := &fakeToolbox{
		definitions: []ToolDefinition{{Name: "write"}},
		presentation: func(snapshot ToolCallSnapshot) ToolPresentation {
			content := "hel"
			if snapshot.Complete {
				content = "hello"
			}
			return ToolPresentation{Title: "write demo.go", Lines: []string{content}}
		},
		executeWithUpdates: func(_ context.Context, _ ToolCall, updates ToolUpdateSink) (ToolResult, error) {
			executed = true
			if err := updates.Update(ToolPresentation{
				Title: "write demo.go", Lines: []string{"written"},
				Diff: []ToolDiffLine{{Kind: ToolDiffLineAdded, NewLine: 1, Text: "written"}},
			}); err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Output: "wrote file"}, nil
		},
	}

	engine := newTestEngine(t, provider, toolbox, Options{Model: "test-model"})
	if _, err := engine.Run(context.Background(), "write", func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []EventKind{EventToolStart, EventToolUpdate, EventContextUsage, EventToolExecute, EventToolUpdate, EventToolEnd, EventContextUsage}
	if got := eventKinds(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if events[0].Presentation.Lines[0] != "hel" || events[1].Presentation.Lines[0] != "hello" || events[4].Presentation.Lines[0] != "written" || events[4].Presentation.Diff[0].Text != "written" || events[5].Presentation.Diff[0].Text != "written" {
		t.Fatalf("presentations = %+v", events)
	}
}

func TestEngineSerializesConcurrentToolSnapshots(t *testing.T) {
	calls := 0
	provider := streamingProviderFunc(func(_ context.Context, _ Request, _ TextSink, _ TextSink, onToolCall ToolCallSink) (Response, error) {
		calls++
		if calls > 1 {
			return Response{Text: "done"}, nil
		}

		var wait sync.WaitGroup
		for _, id := range []string{"one", "two"} {
			wait.Add(1)
			go func() {
				defer wait.Done()
				raw := `{"path":"` + id + `.txt"}`
				if err := onToolCall(ToolCallSnapshot{ID: id, Name: "write", RawArguments: raw, Complete: true}); err != nil {
					t.Errorf("snapshot %s: %v", id, err)
				}
			}()
		}
		wait.Wait()
		return Response{ToolCalls: []ToolCall{
			{ID: "one", Name: "write", Arguments: json.RawMessage(`{"path":"one.txt"}`)},
			{ID: "two", Name: "write", Arguments: json.RawMessage(`{"path":"two.txt"}`)},
		}}, nil
	})
	toolbox := &fakeToolbox{executeWithUpdates: func(_ context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
		var wait sync.WaitGroup
		for _, line := range []string{"first", "second"} {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if err := updates.Update(ToolPresentation{Title: "write " + call.ID, Lines: []string{line}}); err != nil {
					t.Errorf("update %s: %v", call.ID, err)
				}
			}()
		}
		wait.Wait()
		return ToolResult{Output: call.ID}, nil
	}}
	var events []Event
	engine := newTestEngine(t, provider, toolbox, Options{Model: "test-model"})
	if _, err := engine.Run(context.Background(), "write", func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	counts := map[EventKind]int{}
	for _, event := range events {
		counts[event.Kind]++
	}
	if counts[EventToolStart] != 2 || counts[EventToolExecute] != 2 || counts[EventToolEnd] != 2 {
		t.Fatalf("event counts = %v", counts)
	}
}

func TestEngineClosesStreamedToolsInStartOrderOnProviderFailure(t *testing.T) {
	failure := errors.New("stream failed")
	provider := streamingProviderFunc(func(_ context.Context, _ Request, _ TextSink, _ TextSink, onToolCall ToolCallSink) (Response, error) {
		for _, id := range []string{"one", "two"} {
			if err := onToolCall(ToolCallSnapshot{ID: id, Name: "write"}); err != nil {
				return Response{}, err
			}
		}
		return Response{}, failure
	})
	var events []Event
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Model: "test-model"})
	_, err := engine.Run(context.Background(), "write", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Run() error = %v", err)
	}
	if got := eventKinds(events); !slices.Equal(got, []EventKind{EventToolStart, EventToolStart, EventToolEnd, EventToolEnd}) {
		t.Fatalf("events = %v", got)
	}
	if events[2].Call.ID != "one" || events[3].Call.ID != "two" {
		t.Fatalf("end order = %q, %q", events[2].Call.ID, events[3].Call.ID)
	}
}

func TestEngineExecutesMultipleCallsConcurrentlyAndReturnsResultsInProviderOrder(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{
					{ID: "call-b", Name: "second", Arguments: json.RawMessage(`{}`)},
					{ID: "call-a", Name: "first", Arguments: json.RawMessage(`{}`)},
				},
				State: []byte("calls"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			want := []Input{
				{Kind: InputToolResult, CallID: "call-b", Tool: "second", Text: "second result"},
				{Kind: InputToolResult, CallID: "call-a", Tool: "first", Text: "first result"},
			}
			if !reflect.DeepEqual(request.Inputs, want) {
				t.Errorf("tool results = %+v, want %+v", request.Inputs, want)
			}
			return Response{Text: "done", State: []byte("done")}, nil
		},
	}}
	started := make(chan string, 2)
	finished := make(chan string, 2)
	releases := map[string]chan struct{}{
		"call-a": make(chan struct{}),
		"call-b": make(chan struct{}),
	}
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.ID
		<-releases[call.ID]
		finished <- call.ID
		return ToolResult{Output: call.Name + " result"}, nil
	}}

	engine := newTestEngine(t, provider, toolbox, Options{})
	var events []Event
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "run both", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()

	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		select {
		case callID := <-started:
			seen[callID] = true
		case <-time.After(2 * time.Second):
			close(releases["call-a"])
			close(releases["call-b"])
			<-done
			t.Fatal("tool calls did not execute concurrently")
		}
	}

	close(releases["call-a"])
	if callID := <-finished; callID != "call-a" {
		t.Fatalf("first completed call = %q", callID)
	}
	close(releases["call-b"])
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	executing := 0
	for _, event := range events {
		switch event.Kind {
		case EventToolExecute:
			executing++
		case EventToolEnd:
			if executing != 2 {
				t.Fatalf("tool ended after %d execute events: %v", executing, eventKinds(events))
			}
		}
	}
}

func TestEngineCancelsParallelCallsOnUpdateFailureAndPreservesResults(t *testing.T) {
	updateFailure := errors.New("update failed")
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{
					{ID: "update", Name: "update", Arguments: json.RawMessage(`{}`)},
					{ID: "wait", Name: "wait", Arguments: json.RawMessage(`{}`)},
					{ID: "done", Name: "done", Arguments: json.RawMessage(`{}`)},
				},
				State: []byte("calls"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 4 {
				t.Errorf("continued inputs = %+v", request.Inputs)
				return Response{Text: "recovered"}, nil
			}
			results := request.Inputs[:3]
			if results[0].CallID != "update" || !results[0].IsError || !strings.Contains(results[0].Text, updateFailure.Error()) {
				t.Errorf("update result = %+v", results[0])
			}
			if results[1].CallID != "wait" || !results[1].IsError || !strings.Contains(results[1].Text, context.Canceled.Error()) {
				t.Errorf("wait result = %+v", results[1])
			}
			if !reflect.DeepEqual(results[2], Input{Kind: InputToolResult, CallID: "done", Tool: "done", Text: "completed"}) {
				t.Errorf("completed result = %+v", results[2])
			}
			if !reflect.DeepEqual(request.Inputs[3], NewTextInput("continue")) {
				t.Errorf("user input = %+v", request.Inputs[3])
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	started := make(chan string, 3)
	failUpdate := make(chan struct{})
	finishSuccess := make(chan struct{})
	waitCanceled := make(chan struct{})
	toolbox := &fakeToolbox{executeWithUpdates: func(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
		started <- call.ID
		switch call.ID {
		case "update":
			<-failUpdate
			err := updates.Update(ToolPresentation{Title: "update", Lines: []string{"changed"}})
			return ToolResult{Output: "changed"}, err
		case "wait":
			<-ctx.Done()
			close(waitCanceled)
			return ToolResult{}, ctx.Err()
		case "done":
			<-finishSuccess
			return ToolResult{Output: "completed"}, nil
		default:
			return ToolResult{}, fmt.Errorf("unexpected call %q", call.ID)
		}
	}}

	engine := newTestEngine(t, provider, toolbox, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "run", func(event Event) error {
			if event.Kind == EventToolUpdate && event.Call.ID == "update" {
				return updateFailure
			}
			return nil
		})
		done <- err
	}()

	seen := make(map[string]bool, 3)
	for len(seen) < 3 {
		select {
		case callID := <-started:
			seen[callID] = true
		case <-time.After(2 * time.Second):
			close(finishSuccess)
			close(failUpdate)
			<-done
			t.Fatal("parallel tools did not start")
		}
	}
	close(finishSuccess)
	close(failUpdate)

	if err := <-done; !errors.Is(err, updateFailure) {
		t.Fatalf("Run() error = %v, want update failure", err)
	}
	select {
	case <-waitCanceled:
	default:
		t.Fatal("update failure did not cancel sibling tool")
	}

	result, err := engine.Run(context.Background(), "continue", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("continued result = %+v, error = %v", result, err)
	}
}

func TestEngineReturnsUnknownToolsToProvider(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{{ID: "unknown", Name: "missing", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("calls"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || !request.Inputs[0].IsError || request.Inputs[0].CallID != "unknown" || request.Inputs[0].Tool != "missing" {
				t.Fatalf("unknown result = %+v", request.Inputs)
			}
			return Response{Text: "recovered", State: []byte("done")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}}

	engine := newTestEngine(t, provider, toolbox, Options{})
	result, err := engine.Run(context.Background(), "recover", discardEvents)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("result text = %q", result.Text)
	}
}

func TestEngineDoesNotLimitToolRounds(t *testing.T) {
	const rounds = 21

	steps := make([]providerStep, 0, rounds+1)
	for round := 1; round <= rounds; round++ {
		callID := fmt.Sprintf("call-%d", round)
		state := fmt.Appendf(nil, "state-%d", round)
		steps = append(steps, func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{{ID: callID, Name: "tool", Arguments: json.RawMessage(`{}`)}},
				State:     state,
			}, nil
		})
	}
	steps = append(steps, func(_ context.Context, request Request, _ TextSink) (Response, error) {
		if string(request.State) != "state-21" || len(request.Inputs) != 1 || request.Inputs[0].CallID != "call-21" || request.Inputs[0].IsError {
			t.Fatalf("final continuation = %+v", request)
		}
		return Response{Text: "done", State: []byte("done")}, nil
	})

	toolExecutions := 0
	provider := &scriptedProvider{t: t, steps: steps}
	toolbox := &fakeToolbox{execute: func(_ context.Context, _ ToolCall) (ToolResult, error) {
		toolExecutions++
		return ToolResult{Output: "ok"}, nil
	}}

	engine := newTestEngine(t, provider, toolbox, Options{})
	result, err := engine.Run(context.Background(), "loop", discardEvents)
	if err != nil || result.Text != "done" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if toolExecutions != rounds {
		t.Fatalf("tool executions = %d, want %d", toolExecutions, rounds)
	}
}

func TestEngineHonorsCancellationDuringToolExecution(t *testing.T) {
	toolStarted := make(chan struct{})
	var once sync.Once
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "wait", Name: "wait", Arguments: json.RawMessage(`{}`)}}, State: []byte("waiting")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "waiting" || len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].CallID != "wait" || request.Inputs[0].Tool != "wait" || !request.Inputs[0].IsError || request.Inputs[1].Kind != InputUser || request.Inputs[1].PlainText() != "continue" {
				t.Fatalf("state after cancellation = %+v", request)
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		once.Do(func() { close(toolStarted) })
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var events []Event
	go func() {
		_, err := engine.Run(ctx, "wait", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- err
	}()

	select {
	case <-toolStarted:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	if got := eventKinds(events); !slices.Equal(got, []EventKind{EventContextUsage, EventToolStart, EventToolExecute, EventToolEnd}) {
		t.Fatalf("canceled tool events = %v", got)
	}
	if !events[len(events)-1].Result.IsError {
		t.Fatalf("canceled tool result = %+v", events[len(events)-1].Result)
	}
	result, err := engine.Run(context.Background(), "continue", discardEvents)
	if err != nil {
		t.Fatalf("Run() after cancellation error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("Run() after cancellation text = %q", result.Text)
	}
}

func TestEngineResetDiscardsProviderState(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.State) != 0 {
				t.Fatalf("initial state = %q", request.State)
			}
			return Response{Text: "first", State: []byte("saved")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.State) != 0 || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || request.Inputs[0].PlainText() != "second" {
				t.Fatalf("request after reset = %+v", request)
			}
			return Response{Text: "second"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	engine.conversation.inputs = []Input{{Kind: InputToolResult, CallID: "pending", Tool: "write", Text: "pending"}}
	engine.Reset()
	if _, err := engine.Run(context.Background(), "second", discardEvents); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestEngineTreatsToolLocalDeadlineAsRecoverable(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "timeout", Name: "bash", Arguments: json.RawMessage(`{}`)}}, State: []byte("timeout")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].CallID != "timeout" || request.Inputs[0].Tool != "bash" || !request.Inputs[0].IsError {
				t.Fatalf("tool-local deadline result = %+v", request.Inputs)
			}
			return Response{Text: "recovered", State: []byte("done")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{}, context.DeadlineExceeded
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})

	result, err := engine.Run(context.Background(), "run", discardEvents)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("result text = %q", result.Text)
	}
}
