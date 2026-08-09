package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type providerStep func(context.Context, Request, TextSink) (Response, error)

type streamingProviderFunc func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error)

func (function streamingProviderFunc) Generate(ctx context.Context, request Request, observer StreamObserver) (Response, error) {
	return function(ctx, request, observer.Text, observer.Reasoning, observer.ToolCall)
}

type retryingProvider struct {
	generate streamingProviderFunc
	retry    func(error, int) (time.Duration, bool)
}

func (p *retryingProvider) Generate(ctx context.Context, request Request, observer StreamObserver) (Response, error) {
	return p.generate.Generate(ctx, request, observer)
}

func (p *retryingProvider) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return p.retry(err, failedAttempts)
}

type scriptedProvider struct {
	t         *testing.T
	steps     []providerStep
	calls     int
	reasoning string
}

func (p *scriptedProvider) Generate(ctx context.Context, request Request, observer StreamObserver) (Response, error) {
	onText := observer.Text
	onReasoning := observer.Reasoning
	p.t.Helper()
	if p.calls >= len(p.steps) {
		p.t.Fatalf("unexpected provider call %d", p.calls+1)
	}
	if p.calls == 0 && p.reasoning != "" {
		if err := onReasoning(p.reasoning); err != nil {
			return Response{}, err
		}
	}

	step := p.steps[p.calls]
	p.calls++
	return step(ctx, request, onText)
}

type compactingProvider struct {
	Provider
	shouldCompact func(Request, Usage) bool
	compact       func(context.Context, Request) (CompactResponse, error)
}

func (p *compactingProvider) ShouldCompact(request Request, usage Usage) bool {
	return p.shouldCompact != nil && p.shouldCompact(request, usage)
}

func (p *compactingProvider) Compact(ctx context.Context, request Request) (CompactResponse, error) {
	return p.compact(ctx, request)
}

type fakeToolbox struct {
	definitions        []ToolDefinition
	presentation       func(ToolCallSnapshot) ToolPresentation
	execute            func(context.Context, ToolCall) (ToolResult, error)
	executeWithUpdates func(context.Context, ToolCall, ToolUpdateSink) (ToolResult, error)
}

func (t *fakeToolbox) Definitions() []ToolDefinition {
	return slices.Clone(t.definitions)
}

func (t *fakeToolbox) Presentation(snapshot ToolCallSnapshot) ToolPresentation {
	if t.presentation != nil {
		return t.presentation(snapshot)
	}
	return ToolPresentation{Title: snapshot.Name}
}

func (t *fakeToolbox) Execute(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
	if t.executeWithUpdates != nil {
		return t.executeWithUpdates(ctx, call, updates)
	}
	if t.execute == nil {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}

	return t.execute(ctx, call)
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
			if !strings.Contains(request.Instructions, "- read:") || !strings.Contains(request.Instructions, "- write:") {
				t.Fatalf("instructions omit active tools:\n%s", request.Instructions)
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
			if input.Kind != InputToolResult || input.CallID != "call-1" || input.Tool != "read" || input.Text != "file contents" || input.IsError {
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
			{Name: "read", Description: "Read file contents"},
			{Name: "write", Description: "Create or overwrite files"},
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

func TestEngineRetriesGenerationWithoutExecutingPartialToolCalls(t *testing.T) {
	transient := errors.New("temporary provider failure")
	generateCalls := 0
	provider := &retryingProvider{
		generate: func(_ context.Context, request Request, _ TextSink, _ TextSink, onToolCall ToolCallSink) (Response, error) {
			generateCalls++
			switch generateCalls {
			case 1:
				assertUserInput(t, request, "write the file")
				if err := onToolCall(ToolCallSnapshot{ID: "abandoned", Name: "write", RawArguments: `{"path":"old.txt"}`}); err != nil {
					return Response{}, err
				}
				return Response{}, transient
			case 2:
				assertUserInput(t, request, "write the file")
				return Response{
					State: []byte("tool-state"),
					ToolCalls: []ToolCall{{
						ID: "completed", Name: "write", Arguments: json.RawMessage(`{"path":"new.txt"}`),
					}},
				}, nil
			case 3:
				if string(request.State) != "tool-state" || len(request.Inputs) != 1 || request.Inputs[0].CallID != "completed" {
					t.Fatalf("post-tool request = %+v", request)
				}
				return Response{Text: "done", State: []byte("done-state")}, nil
			default:
				t.Fatalf("unexpected provider call %d", generateCalls)
				return Response{}, nil
			}
		},
		retry: func(err error, failedAttempts int) (time.Duration, bool) {
			return 0, errors.Is(err, transient) && failedAttempts == 1
		},
	}

	executions := 0
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions++
		if call.ID != "completed" {
			t.Fatalf("executed abandoned call: %+v", call)
		}
		return ToolResult{Output: "written"}, nil
	}}
	var events []Event
	engine := newTestEngine(t, provider, toolbox, Options{})
	result, err := engine.Run(context.Background(), "write the file", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || result.Text != "done" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	if generateCalls != 3 || executions != 1 {
		t.Fatalf("generate calls = %d, executions = %d", generateCalls, executions)
	}
	if got := eventKinds(events); !slices.Equal(got, []EventKind{
		EventToolStart, EventToolEnd,
		EventContextUsage, EventToolStart, EventToolExecute, EventToolEnd,
		EventContextUsage,
	}) {
		t.Fatalf("events = %v", got)
	}
	if !events[1].Result.IsError || !strings.Contains(events[1].Result.Output, transient.Error()) {
		t.Fatalf("abandoned tool result = %+v", events[1].Result)
	}
}

func TestEngineStopsGenerationRetryBackoffWhenCanceled(t *testing.T) {
	transient := errors.New("temporary provider failure")
	policyCalled := make(chan struct{})
	generateCalls := 0
	provider := &retryingProvider{
		generate: func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
			generateCalls++
			return Response{}, transient
		},
		retry: func(error, int) (time.Duration, bool) {
			close(policyCalled)
			return time.Hour, true
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, "start", discardEvents)
		done <- err
	}()

	<-policyCalled
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if generateCalls != 1 {
		t.Fatalf("generate calls = %d", generateCalls)
	}
}

func TestEngineGenerationFailurePreservesPriorContextAndUserInput(t *testing.T) {
	failure := errors.New("generation failed")
	calls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		calls++
		switch calls {
		case 1:
			if string(request.State) != "stable" || len(request.Inputs) != 1 || request.Inputs[0].Text != "first question" {
				t.Fatalf("failed request = %+v", request)
			}
			return Response{}, failure
		case 2:
			if string(request.State) != "stable" || len(request.Inputs) != 2 || request.Inputs[0].Text != "first question" || request.Inputs[1].Text != "second question" {
				t.Fatalf("continued request = %+v", request)
			}
			return Response{Text: "continued", State: []byte("continued")}, nil
		default:
			t.Fatalf("unexpected provider call %d", calls)
			return Response{}, nil
		}
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	engine.state = []byte("stable")

	if _, err := engine.Run(context.Background(), "first question", discardEvents); !errors.Is(err, failure) {
		t.Fatalf("first Run() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "second question", discardEvents)
	if err != nil || result.Text != "continued" {
		t.Fatalf("continued result = %+v, error = %v", result, err)
	}
}

func TestEngineCompactionSinkFailuresPreservePhaseContinuation(t *testing.T) {
	failure := errors.New("sink failed")
	for _, test := range []struct {
		name       string
		failKind   EventKind
		wantState  string
		wantUsage  Usage
		wantInputs []Input
	}{
		{
			name:      "start",
			failKind:  EventCompactionStart,
			wantState: "stable",
			wantUsage: Usage{TotalTokens: 100},
			wantInputs: []Input{
				{Kind: InputUser, Text: "first"},
				{Kind: InputUser, Text: "continue"},
			},
		},
		{
			name:      "end",
			failKind:  EventCompactionEnd,
			wantState: "compact",
			wantInputs: []Input{
				{Kind: InputUser, Text: "continue"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			generateCalls := 0
			provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
				generateCalls++
				if string(request.State) != test.wantState || !slices.Equal(request.Inputs, test.wantInputs) {
					t.Fatalf("continued request = %+v", request)
				}
				return Response{Text: "recovered", State: []byte("recovered")}, nil
			})
			compactChecks := 0
			compacting := &compactingProvider{
				Provider: provider,
				shouldCompact: func(request Request, usage Usage) bool {
					compactChecks++
					if compactChecks > 1 && usage != test.wantUsage {
						t.Fatalf("continued usage = %+v, want %+v", usage, test.wantUsage)
					}
					return compactChecks == 1
				},
				compact: func(context.Context, Request) (CompactResponse, error) {
					return CompactResponse{State: []byte("compact"), Usage: Usage{TotalTokens: 42}}, nil
				},
			}
			engine := newTestEngine(t, compacting, &fakeToolbox{}, Options{})
			engine.state = []byte("stable")
			engine.contextUsage = Usage{TotalTokens: 100}

			failed := false
			_, err := engine.Run(context.Background(), "first", func(event Event) error {
				if event.Kind == test.failKind && !failed {
					failed = true
					return failure
				}
				return nil
			})
			if !errors.Is(err, failure) {
				t.Fatalf("Run() error = %v, want sink failure", err)
			}
			result, err := engine.Run(context.Background(), "continue", discardEvents)
			if err != nil || result.Text != "recovered" || generateCalls != 1 {
				t.Fatalf("continued result = %+v, error = %v, generate calls = %d", result, err, generateCalls)
			}
		})
	}
}

func TestEngineEventSinkFailuresPreserveResponseContinuation(t *testing.T) {
	failure := errors.New("sink failed")
	synthetic := func(id, tool string) Input {
		return Input{Kind: InputToolResult, CallID: id, Tool: tool, Text: "tool was not executed: " + failure.Error(), IsError: true}
	}

	for _, test := range []struct {
		name         string
		failKind     EventKind
		response     Response
		stream       func(ToolCallSink) error
		toolbox      *fakeToolbox
		wantPending  []Input
		wantExecuted int
	}{
		{
			name:     "context usage",
			failKind: EventContextUsage,
			response: Response{State: []byte("calls"), ToolCalls: []ToolCall{
				{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)},
				{ID: "two", Name: "read", Arguments: json.RawMessage(`{}`)},
			}},
			toolbox:     &fakeToolbox{},
			wantPending: []Input{synthetic("one", "write"), synthetic("two", "read")},
		},
		{
			name:     "tool start",
			failKind: EventToolStart,
			response: Response{State: []byte("calls"), ToolCalls: []ToolCall{
				{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)},
				{ID: "two", Name: "read", Arguments: json.RawMessage(`{}`)},
			}},
			toolbox:     &fakeToolbox{},
			wantPending: []Input{synthetic("one", "write"), synthetic("two", "read")},
		},
		{
			name:     "final tool update",
			failKind: EventToolUpdate,
			response: Response{State: []byte("calls"), ToolCalls: []ToolCall{
				{ID: "one", Name: "write", Arguments: json.RawMessage(`{"content":"complete"}`)},
			}},
			stream: func(sink ToolCallSink) error {
				return sink(ToolCallSnapshot{ID: "one", Name: "write", RawArguments: `{"content":"partial"}`})
			},
			toolbox: &fakeToolbox{presentation: func(snapshot ToolCallSnapshot) ToolPresentation {
				return ToolPresentation{Title: "write", Lines: []string{snapshot.RawArguments}}
			}},
			wantPending: []Input{synthetic("one", "write")},
		},
		{
			name:        "tool execute",
			failKind:    EventToolExecute,
			response:    Response{State: []byte("calls"), ToolCalls: []ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)}}},
			toolbox:     &fakeToolbox{},
			wantPending: []Input{synthetic("one", "write")},
		},
		{
			name:     "tool progress update",
			failKind: EventToolUpdate,
			response: Response{State: []byte("calls"), ToolCalls: []ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)}}},
			toolbox: &fakeToolbox{executeWithUpdates: func(_ context.Context, _ ToolCall, updates ToolUpdateSink) (ToolResult, error) {
				_ = updates.Update(ToolPresentation{Title: "write", Lines: []string{"changed"}})
				return ToolResult{Output: "changed"}, nil
			}},
			wantPending:  []Input{{Kind: InputToolResult, CallID: "one", Tool: "write", Text: "changed", IsError: true}},
			wantExecuted: 1,
		},
		{
			name:     "tool end",
			failKind: EventToolEnd,
			response: Response{State: []byte("calls"), ToolCalls: []ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)}}},
			toolbox: &fakeToolbox{executeWithUpdates: func(_ context.Context, _ ToolCall, updates ToolUpdateSink) (ToolResult, error) {
				updates.SetFinal(ToolPresentation{
					Title: "edit", Diff: []ToolDiffLine{{Kind: ToolDiffLineAdded, NewLine: 1, Text: "changed"}},
				})
				return ToolResult{Output: "changed"}, nil
			}},
			wantPending:  []Input{{Kind: InputToolResult, CallID: "one", Tool: "write", Text: "changed"}},
			wantExecuted: 1,
		},
		{
			name:     "incomplete streamed tool end",
			failKind: EventToolEnd,
			response: Response{State: []byte("complete")},
			stream: func(sink ToolCallSink) error {
				return sink(ToolCallSnapshot{ID: "ghost", Name: "write", RawArguments: `{}`})
			},
			toolbox: &fakeToolbox{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			providerCalls := 0
			executed := 0
			if test.toolbox.execute != nil {
				execute := test.toolbox.execute
				test.toolbox.execute = func(ctx context.Context, call ToolCall) (ToolResult, error) {
					executed++
					return execute(ctx, call)
				}
			}
			if test.toolbox.executeWithUpdates != nil {
				execute := test.toolbox.executeWithUpdates
				test.toolbox.executeWithUpdates = func(ctx context.Context, call ToolCall, updates ToolUpdateSink) (ToolResult, error) {
					executed++
					return execute(ctx, call, updates)
				}
			}
			provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, onToolCall ToolCallSink) (Response, error) {
				providerCalls++
				if providerCalls == 1 {
					if test.stream != nil {
						if err := test.stream(onToolCall); err != nil {
							return Response{}, err
						}
					}
					return test.response, nil
				}

				wantInputs := append([]Input(nil), test.wantPending...)
				wantInputs = append(wantInputs, Input{Kind: InputUser, Text: "continue"})
				if string(request.State) != string(test.response.State) || !slices.Equal(request.Inputs, wantInputs) {
					t.Fatalf("continued request = %+v, want state %q and inputs %+v", request, test.response.State, wantInputs)
				}
				return Response{Text: "recovered", State: []byte("recovered")}, nil
			})
			engine := newTestEngine(t, provider, test.toolbox, Options{})
			failed := false
			_, err := engine.Run(context.Background(), "first", func(event Event) error {
				if event.Kind == test.failKind && !failed {
					failed = true
					return failure
				}
				return nil
			})
			if !errors.Is(err, failure) {
				t.Fatalf("Run() error = %v, want sink failure", err)
			}
			result, err := engine.Run(context.Background(), "continue", discardEvents)
			if err != nil || result.Text != "recovered" || executed != test.wantExecuted {
				t.Fatalf("continued result = %+v, error = %v, executions = %d", result, err, executed)
			}
		})
	}
}

func TestEngineCancellationAfterCompletedToolRoundPreservesResult(t *testing.T) {
	providerCalls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		providerCalls++
		if providerCalls == 1 {
			return Response{State: []byte("calls"), ToolCalls: []ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		wantInputs := []Input{
			{Kind: InputToolResult, CallID: "one", Tool: "write", Text: "changed"},
			{Kind: InputUser, Text: "continue"},
		}
		if string(request.State) != "calls" || !slices.Equal(request.Inputs, wantInputs) {
			t.Fatalf("continued request = %+v", request)
		}
		return Response{Text: "recovered", State: []byte("recovered")}, nil
	})
	toolExecutions := 0
	toolbox := &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		toolExecutions++
		return ToolResult{Output: "changed"}, nil
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	_, err := engine.Run(ctx, "first", func(event Event) error {
		if event.Kind == EventToolEnd {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	result, err := engine.Run(context.Background(), "continue", discardEvents)
	if err != nil || result.Text != "recovered" || toolExecutions != 1 {
		t.Fatalf("continued result = %+v, error = %v, executions = %d", result, err, toolExecutions)
	}
}

func TestEngineCompactsBeforeNextUserGeneration(t *testing.T) {
	scripted := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "first")
			return Response{Text: "first answer", State: []byte("full state"), Usage: Usage{InputTokens: 90, OutputTokens: 9, TotalTokens: 99}}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "compact state" || len(request.Inputs) != 0 {
				t.Fatalf("request after compaction = %+v", request)
			}
			return Response{Text: "second answer", State: []byte("next state"), Usage: Usage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23}}, nil
		},
	}}
	compactCalls := 0
	provider := &compactingProvider{
		Provider: scripted,
		shouldCompact: func(request Request, usage Usage) bool {
			return string(request.State) == "full state" && usage.TotalTokens == 99
		},
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			compactCalls++
			if string(request.State) != "full state" {
				t.Fatalf("compact state = %q", request.State)
			}
			assertUserInput(t, request, "next")
			return CompactResponse{State: []byte("compact state"), Usage: Usage{InputTokens: 100, OutputTokens: 5, TotalTokens: 105}}, nil
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	var events []Event
	result, err := engine.Run(context.Background(), "next", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result.Text != "second answer" || result.Usage != (Usage{InputTokens: 120, OutputTokens: 8, TotalTokens: 128}) || compactCalls != 1 {
		t.Fatalf("result = %+v, compact calls = %d", result, compactCalls)
	}
	if got := eventKinds(events); !slices.Equal(got, []EventKind{EventCompactionStart, EventCompactionEnd, EventContextUsage}) {
		t.Fatalf("event kinds = %v", got)
	}
	if events[1].Usage.TotalTokens != 105 {
		t.Fatalf("compaction usage event = %+v", events[1])
	}
	if events[2].Usage.TotalTokens != 23 {
		t.Fatalf("context usage event = %+v", events[2])
	}
}

func TestEngineCompactsToolContinuation(t *testing.T) {
	scripted := &scriptedProvider{t: t, steps: []providerStep{
		func(context.Context, Request, TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tool call state"),
				Usage:     Usage{TotalTokens: 100},
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "compact tool state" || len(request.Inputs) != 0 {
				t.Fatalf("tool continuation after compaction = %+v", request)
			}
			return Response{Text: "done", State: []byte("done")}, nil
		},
	}}
	provider := &compactingProvider{
		Provider: scripted,
		shouldCompact: func(request Request, usage Usage) bool {
			return string(request.State) == "tool call state" && usage.TotalTokens == 100
		},
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].Text != "contents" {
				t.Fatalf("compact tool request = %+v", request)
			}
			return CompactResponse{State: []byte("compact tool state")}, nil
		},
	}
	toolbox := &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "contents"}, nil
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})

	result, err := engine.Run(context.Background(), "inspect", discardEvents)
	if err != nil || result.Text != "done" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestEngineCompactionFailureAfterToolPreservesContinuation(t *testing.T) {
	compactError := errors.New("compact unavailable")
	scripted := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "stable" {
				t.Fatalf("tool request state = %q", request.State)
			}
			return Response{
				ToolCalls: []ToolCall{{ID: "call-1", Name: "write", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tool call state"),
				Usage:     Usage{TotalTokens: 100},
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if string(request.State) != "tool call state" || len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].Text != "changed file" || request.Inputs[1].Kind != InputUser || request.Inputs[1].Text != "continue" {
				t.Fatalf("preserved continuation request = %+v", request)
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	compactionAttempted := false
	provider := &compactingProvider{
		Provider: scripted,
		shouldCompact: func(request Request, _ Usage) bool {
			return !compactionAttempted && string(request.State) == "tool call state"
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			compactionAttempted = true
			return CompactResponse{}, compactError
		},
	}
	toolbox := &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "changed file"}, nil
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})
	engine.state = []byte("stable")
	engine.contextUsage = Usage{TotalTokens: 50}

	_, err := engine.Run(context.Background(), "change", discardEvents)
	if !errors.Is(err, compactError) || !strings.Contains(err.Error(), "agent: compact context") {
		t.Fatalf("Run() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "continue", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("continued result = %+v, error = %v", result, err)
	}
}

func TestEngineResetClearsCompactionUsage(t *testing.T) {
	scripted := &scriptedProvider{t: t, steps: []providerStep{
		func(context.Context, Request, TextSink) (Response, error) {
			return Response{Text: "first", State: []byte("state"), Usage: Usage{TotalTokens: 100}}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.State) != 0 {
				t.Fatalf("state after reset = %q", request.State)
			}
			return Response{Text: "second"}, nil
		},
	}}
	provider := &compactingProvider{
		Provider: scripted,
		shouldCompact: func(request Request, usage Usage) bool {
			if len(request.State) == 0 && usage != (Usage{}) {
				t.Fatalf("usage without state = %+v", usage)
			}
			return false
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			t.Fatal("unexpected compaction")
			return CompactResponse{}, nil
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatal(err)
	}
	engine.Reset()
	if _, err := engine.Run(context.Background(), "second", discardEvents); err != nil {
		t.Fatal(err)
	}
}

func TestEngineExecutesMultipleCallsInProviderOrder(t *testing.T) {
	var executionOrder []string
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
			if len(request.Inputs) != 2 {
				t.Fatalf("tool result input count = %d, want 2", len(request.Inputs))
			}
			gotIDs := []string{request.Inputs[0].CallID, request.Inputs[1].CallID}
			if !slices.Equal(gotIDs, []string{"call-b", "call-a"}) {
				t.Fatalf("tool result IDs = %v", gotIDs)
			}
			return Response{Text: "done", State: []byte("done")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		executionOrder = append(executionOrder, call.Name)
		return ToolResult{Output: call.Name + " result"}, nil
	}}

	engine := newTestEngine(t, provider, toolbox, Options{})
	if _, err := engine.Run(context.Background(), "run both", discardEvents); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !slices.Equal(executionOrder, []string{"second", "first"}) {
		t.Fatalf("execution order = %v", executionOrder)
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
			if len(request.Inputs) != 1 || !request.Inputs[0].IsError || request.Inputs[0].CallID != "unknown" || !strings.Contains(request.Inputs[0].Text, "unknown tool") {
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
			if string(request.State) != "waiting" || len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].CallID != "wait" || !request.Inputs[0].IsError || !strings.Contains(request.Inputs[0].Text, "canceled") || request.Inputs[1].Kind != InputUser || request.Inputs[1].Text != "continue" {
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
			if len(request.State) != 0 || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || request.Inputs[0].Text != "second" {
				t.Fatalf("request after reset = %+v", request)
			}
			return Response{Text: "second"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	engine.pendingInputs = []Input{{Kind: InputToolResult, CallID: "pending", Tool: "write", Text: "pending"}}
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
			if len(request.Inputs) != 1 || !request.Inputs[0].IsError || !strings.Contains(request.Inputs[0].Text, context.DeadlineExceeded.Error()) {
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

func TestEngineQueuesSteeringDuringGenerationOneAtATime(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "initial", State: []byte("one")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "steer one")
			if string(request.State) != "one" {
				t.Fatalf("first steering state = %q", request.State)
			}
			return Response{Text: "handled one", State: []byte("two")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "steer two")
			if string(request.State) != "two" {
				t.Fatalf("second steering state = %q", request.State)
			}
			return Response{Text: "handled two", State: []byte("three")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	var events []Event
	done := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := engine.Run(context.Background(), "start", func(event Event) error {
			events = append(events, event)
			return nil
		})
		done <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	<-started
	if !engine.Steer("steer one") || !engine.Steer("steer two") {
		t.Fatal("active engine rejected steering")
	}
	close(release)
	outcome := <-done
	if outcome.err != nil || outcome.result.Text != "handled two" {
		t.Fatalf("result = %+v, error = %v", outcome.result, outcome.err)
	}
	var delivered []string
	for _, event := range events {
		if event.Kind == EventSteering {
			delivered = append(delivered, event.Text)
		}
	}
	if !slices.Equal(delivered, []string{"steer one", "steer two"}) {
		t.Fatalf("delivered steering = %q", delivered)
	}
	if engine.Steer("too late") {
		t.Fatal("completed engine accepted steering")
	}
}

func TestEngineDeliversSteeringAfterCompleteToolBatch(t *testing.T) {
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
				State: []byte("tools"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			want := []Input{
				{Kind: InputToolResult, CallID: "one", Tool: "tool", Text: "result one"},
				{Kind: InputToolResult, CallID: "two", Tool: "tool", Text: "result two"},
				{Kind: InputUser, Text: "redirect"},
			}
			if string(request.State) != "tools" || !slices.Equal(request.Inputs, want) {
				t.Fatalf("steered tool continuation = %+v", request)
			}
			return Response{Text: "redirected", State: []byte("done")}, nil
		},
	}}
	executions := 0
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions++
		if executions == 1 {
			close(toolStarted)
			<-releaseTool
		}
		return ToolResult{Output: "result " + call.ID}, nil
	}}
	engine := newTestEngine(t, provider, toolbox, Options{})
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
	if !engine.Steer("redirect") {
		t.Fatal("engine rejected steering during tool execution")
	}
	close(releaseTool)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("tool executions = %d", executions)
	}
	steeringIndex := -1
	toolEnds := 0
	for index, event := range events {
		switch event.Kind {
		case EventToolEnd:
			toolEnds++
		case EventSteering:
			steeringIndex = index
			if toolEnds != 2 {
				t.Fatalf("steering delivered after %d tool results", toolEnds)
			}
		}
	}
	if steeringIndex < 0 {
		t.Fatal("missing steering delivery event")
	}
}

func TestEngineDoesNotCheckpointSteeringWhenDeliveryEventFails(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sinkErr := errors.New("sink failed")
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "initial", State: []byte("initial")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "steer")
			if string(request.State) != "initial" {
				t.Fatalf("recovery state = %q", request.State)
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event Event) error {
			if event.Kind == EventSteering {
				return sinkErr
			}
			return nil
		})
		done <- err
	}()

	<-started
	if !engine.Steer("steer") {
		t.Fatal("active engine rejected steering")
	}
	close(release)
	if err := <-done; !errors.Is(err, sinkErr) {
		t.Fatalf("Run() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "steer", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("recovery result = %+v, error = %v", result, err)
	}
}

func TestEnginePreservesToolResultsWhenSteeringDeliveryEventFails(t *testing.T) {
	sinkErr := errors.New("sink failed")
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			return Response{
				ToolCalls: []ToolCall{{ID: "tool", Name: "tool", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tools"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			want := []Input{
				{Kind: InputToolResult, CallID: "tool", Tool: "tool", Text: "result"},
				{Kind: InputUser, Text: "steer"},
			}
			if string(request.State) != "tools" || !slices.Equal(request.Inputs, want) {
				t.Fatalf("recovery request = %+v", request)
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "result"}, nil
	}}, Options{})
	queued := false
	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		switch event.Kind {
		case EventToolExecute:
			queued = engine.Steer("steer")
		case EventSteering:
			return sinkErr
		}
		return nil
	})
	if !queued {
		t.Fatal("active engine rejected steering")
	}
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Run() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "steer", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("recovery result = %+v, error = %v", result, err)
	}
}

func TestEngineClearsQueuedSteeringAfterCancellationAndReset(t *testing.T) {
	started := make(chan struct{})
	provider := streamingProviderFunc(func(ctx context.Context, _ Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		close(started)
		<-ctx.Done()
		return Response{}, ctx.Err()
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, "start", discardEvents)
		done <- err
	}()

	<-started
	if !engine.Steer("queued") {
		t.Fatal("active engine rejected steering")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("stale steering = %q", queued)
	}
	if engine.Steer("late") {
		t.Fatal("canceled engine accepted steering")
	}
	if err := engine.Reset(); err != nil {
		t.Fatal(err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("reset steering = %q", queued)
	}
}

func TestEngineClearsQueuedSteeringAfterFailure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	failure := errors.New("provider failed")
	calls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return Response{}, failure
		}
		want := []Input{{Kind: InputUser, Text: "start"}, {Kind: InputUser, Text: "next"}}
		if !slices.Equal(request.Inputs, want) {
			t.Fatalf("recovery inputs = %+v", request.Inputs)
		}
		return Response{Text: "recovered"}, nil
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", discardEvents)
		done <- err
	}()

	<-started
	if !engine.Steer("queued") {
		t.Fatal("active engine rejected steering")
	}
	close(release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("Run() error = %v", err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("stale steering = %q", queued)
	}
	if engine.Steer("late") {
		t.Fatal("failed engine accepted steering")
	}
	result, err := engine.Run(context.Background(), "next", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("recovery result = %+v, error = %v", result, err)
	}
}

func TestEngineRejectsConcurrentOperations(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := streamingProviderFunc(func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
		close(started)
		<-release
		return Response{Text: "done"}, nil
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "first", discardEvents)
		done <- err
	}()
	<-started

	if _, err := engine.Run(context.Background(), "second", discardEvents); !errors.Is(err, errEngineBusy) {
		t.Fatalf("concurrent Run() error = %v", err)
	}
	if err := engine.Reset(); !errors.Is(err, errEngineBusy) {
		t.Fatalf("concurrent Reset() error = %v", err)
	}
	if err := engine.SetThinkingLevel(ThinkingHigh); !errors.Is(err, errEngineBusy) {
		t.Fatalf("concurrent SetThinkingLevel() error = %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := engine.Reset(); err != nil {
		t.Fatalf("Reset() after Run = %v", err)
	}
}

func TestEngineSendsCurrentThinkingLevel(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if request.ThinkingLevel != DefaultThinkingLevel {
				t.Fatalf("default thinking level = %q", request.ThinkingLevel)
			}
			return Response{Text: "first"}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if request.ThinkingLevel != ThinkingHigh {
				t.Fatalf("updated thinking level = %q", request.ThinkingLevel)
			}
			return Response{Text: "second"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if _, err := engine.Run(context.Background(), "first", discardEvents); err != nil {
		t.Fatal(err)
	}
	if err := engine.SetThinkingLevel(ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(context.Background(), "second", discardEvents); err != nil {
		t.Fatal(err)
	}
	if err := engine.SetThinkingLevel("extreme"); err == nil {
		t.Fatal("invalid thinking level accepted")
	}
}

func TestEngineRejectsPreCanceledContext(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(context.Context, Request, TextSink) (Response, error) {
			t.Fatal("provider called for an already canceled run")
			return Response{}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := engine.Run(ctx, "canceled", discardEvents); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func newTestEngine(t *testing.T, provider Provider, toolbox Toolbox, options Options) *Engine {
	t.Helper()
	return New(provider, toolbox, options)
}

func discardEvents(Event) error { return nil }

func assertUserInput(t *testing.T, request Request, text string) {
	t.Helper()
	if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || request.Inputs[0].Text != text {
		t.Fatalf("user inputs = %+v, want one user input %q", request.Inputs, text)
	}
}

func toolNames(definitions []ToolDefinition) []string {
	names := make([]string, len(definitions))
	for i, definition := range definitions {
		names[i] = definition.Name
	}

	return names
}

func eventKinds(events []Event) []EventKind {
	kinds := make([]EventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind
	}

	return kinds
}
