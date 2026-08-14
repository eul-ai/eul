package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestEngineGenerationFailurePreservesPriorContextAndUserInput(t *testing.T) {
	failure := errors.New("generation failed")
	calls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		calls++
		switch calls {
		case 1:
			if string(request.State) != "stable" || len(request.Inputs) != 1 || request.Inputs[0].PlainText() != "first question" {
				t.Fatalf("failed request = %+v", request)
			}
			return Response{}, failure
		case 2:
			if string(request.State) != "stable" || len(request.Inputs) != 2 || request.Inputs[0].PlainText() != "first question" || request.Inputs[1].PlainText() != "second question" {
				t.Fatalf("continued request = %+v", request)
			}
			return Response{Text: "continued", State: []byte("continued")}, nil
		default:
			t.Fatalf("unexpected provider call %d", calls)
			return Response{}, nil
		}
	})
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	engine.conversation.state = []byte("stable")

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
				NewTextInput("first"),
				NewTextInput("continue"),
			},
		},
		{
			name:      "end",
			failKind:  EventCompactionEnd,
			wantState: "compact",
			wantInputs: []Input{
				NewTextInput("continue"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			generateCalls := 0
			provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
				generateCalls++
				if string(request.State) != test.wantState || !reflect.DeepEqual(request.Inputs, test.wantInputs) {
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
			engine.conversation.state = []byte("stable")
			engine.conversation.usage = Usage{TotalTokens: 100}

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
		return Input{Kind: InputToolResult, CallID: id, Tool: tool, IsError: true}
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

				if string(request.State) != string(test.response.State) || len(request.Inputs) != len(test.wantPending)+1 {
					t.Fatalf("continued request = %+v, want state %q and %d pending inputs", request, test.response.State, len(test.wantPending))
				}
				for i, want := range test.wantPending {
					got := request.Inputs[i]
					if got.Kind != want.Kind || got.CallID != want.CallID || got.Tool != want.Tool || got.IsError != want.IsError || (want.Text != "" && got.Text != want.Text) {
						t.Fatalf("pending input %d = %+v, want fields %+v", i, got, want)
					}
				}
				if got := request.Inputs[len(test.wantPending)]; !reflect.DeepEqual(got, NewTextInput("continue")) {
					t.Fatalf("continuation input = %+v", got)
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
			NewTextInput("continue"),
		}
		if string(request.State) != "calls" || !reflect.DeepEqual(request.Inputs, wantInputs) {
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
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].PlainText() != "contents" {
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

func TestEngineCompactsToolContinuationAfterGenerationError(t *testing.T) {
	contextLimit := errors.New("context limit exceeded")
	generateCalls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		generateCalls++
		switch generateCalls {
		case 1:
			assertUserInput(t, request, "inspect")
			return Response{
				ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)}},
				State:     []byte("tool state"),
				Usage:     Usage{InputTokens: 70, OutputTokens: 10, TotalTokens: 80},
			}, nil
		case 2:
			if string(request.State) != "tool state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].PlainText() != "large tool output" {
				t.Fatalf("failed tool continuation = %+v", request)
			}
			return Response{}, contextLimit
		case 3:
			if string(request.State) != "compact state" || len(request.Inputs) != 0 {
				t.Fatalf("request after error compaction = %+v", request)
			}
			return Response{
				Text:  "done",
				State: []byte("done state"),
				Usage: Usage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23},
			}, nil
		default:
			t.Fatalf("unexpected provider call %d", generateCalls)
			return Response{}, nil
		}
	})
	compactCalls := 0
	compacting := &compactingProvider{
		Provider: provider,
		shouldCompact: func(Request, Usage) bool {
			return false
		},
		shouldCompactAfterError: func(request Request, err error) bool {
			if string(request.State) != "tool state" || len(request.Inputs) != 1 {
				t.Fatalf("error compaction policy request = %+v", request)
			}
			return errors.Is(err, contextLimit)
		},
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			compactCalls++
			if string(request.State) != "tool state" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].PlainText() != "large tool output" {
				t.Fatalf("error compaction request = %+v", request)
			}
			return CompactResponse{
				State: []byte("compact state"),
				Usage: Usage{InputTokens: 85, OutputTokens: 5, TotalTokens: 90},
			}, nil
		},
	}
	toolbox := &fakeToolbox{execute: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{Output: "large tool output"}, nil
	}}
	var events []Event
	engine := newTestEngine(t, compacting, toolbox, Options{})
	result, err := engine.Run(context.Background(), "inspect", func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "done" || result.Usage != (Usage{InputTokens: 175, OutputTokens: 18, TotalTokens: 193}) || generateCalls != 3 || compactCalls != 1 {
		t.Fatalf("result = %+v, generate calls = %d, compact calls = %d", result, generateCalls, compactCalls)
	}
	if got := eventKinds(events); !slices.Equal(got, []EventKind{
		EventContextUsage,
		EventToolStart, EventToolExecute, EventToolEnd,
		EventCompactionStart, EventCompactionEnd,
		EventContextUsage,
	}) {
		t.Fatalf("events = %v", got)
	}
}

func TestEngineAttemptsErrorCompactionOnce(t *testing.T) {
	contextLimit := errors.New("context limit exceeded")
	generateCalls := 0
	provider := streamingProviderFunc(func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
		generateCalls++
		return Response{}, contextLimit
	})
	compactCalls := 0
	compacting := &compactingProvider{
		Provider: provider,
		shouldCompactAfterError: func(Request, error) bool {
			return true
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			compactCalls++
			return CompactResponse{State: []byte("compact state")}, nil
		},
	}
	engine := newTestEngine(t, compacting, &fakeToolbox{}, Options{})

	_, err := engine.Run(context.Background(), "hello", discardEvents)
	if !errors.Is(err, contextLimit) {
		t.Fatalf("Run() error = %v", err)
	}
	if generateCalls != 2 || compactCalls != 1 || string(engine.conversation.state) != "compact state" || len(engine.conversation.inputs) != 0 {
		t.Fatalf("generate calls = %d, compact calls = %d, state = %q, pending = %+v", generateCalls, compactCalls, engine.conversation.state, engine.conversation.inputs)
	}
}

func TestEngineErrorCompactionFailurePreservesContinuation(t *testing.T) {
	contextLimit := errors.New("context limit exceeded")
	compactFailure := errors.New("compact unavailable")
	provider := streamingProviderFunc(func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
		return Response{}, contextLimit
	})
	compacting := &compactingProvider{
		Provider: provider,
		shouldCompactAfterError: func(Request, error) bool {
			return true
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			return CompactResponse{}, compactFailure
		},
	}
	engine := newTestEngine(t, compacting, &fakeToolbox{}, Options{})
	engine.conversation.state = []byte("stable")
	engine.conversation.usage = Usage{TotalTokens: 100}

	_, err := engine.Run(context.Background(), "hello", discardEvents)
	if !errors.Is(err, compactFailure) {
		t.Fatalf("Run() error = %v", err)
	}
	if string(engine.conversation.state) != "stable" || engine.conversation.usage.TotalTokens != 100 || len(engine.conversation.inputs) != 1 || engine.conversation.inputs[0].PlainText() != "hello" {
		t.Fatalf("checkpoint = state %q, usage %+v, pending %+v", engine.conversation.state, engine.conversation.usage, engine.conversation.inputs)
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
			if string(request.State) != "tool call state" || len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].PlainText() != "changed file" || request.Inputs[1].Kind != InputUser || request.Inputs[1].PlainText() != "continue" {
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
	engine.conversation.state = []byte("stable")
	engine.conversation.usage = Usage{TotalTokens: 50}

	_, err := engine.Run(context.Background(), "change", discardEvents)
	if !errors.Is(err, compactError) {
		t.Fatalf("Run() error = %v", err)
	}
	result, err := engine.Run(context.Background(), "continue", discardEvents)
	if err != nil || result.Text != "recovered" {
		t.Fatalf("continued result = %+v, error = %v", result, err)
	}
}

func TestEngineCompactsOnDemand(t *testing.T) {
	compactCalls := 0
	provider := &compactingProvider{
		compact: func(_ context.Context, request Request) (CompactResponse, error) {
			compactCalls++
			if string(request.State) != "stable" || len(request.Inputs) != 1 || request.Inputs[0].Kind != InputToolResult || request.Inputs[0].PlainText() != "large output" {
				t.Fatalf("compact request = %+v", request)
			}
			return CompactResponse{
				State: []byte("compact state"),
				Usage: Usage{InputTokens: 100, OutputTokens: 5, TotalTokens: 105},
			}, nil
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	engine.conversation.state = []byte("stable")
	engine.conversation.usage = Usage{TotalTokens: 90}
	engine.conversation.inputs = []Input{{Kind: InputToolResult, Text: "large output", CallID: "call-1", Tool: "read"}}
	var events []Event

	err := engine.Compact(context.Background(), func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if compactCalls != 1 || string(engine.conversation.state) != "compact state" || engine.conversation.usage != (Usage{}) || len(engine.conversation.inputs) != 0 {
		t.Fatalf("compact calls = %d, state = %q, usage = %+v, pending = %+v", compactCalls, engine.conversation.state, engine.conversation.usage, engine.conversation.inputs)
	}
	if got := eventKinds(events); !slices.Equal(got, []EventKind{EventCompactionStart, EventCompactionEnd}) {
		t.Fatalf("events = %v", got)
	}
	if events[1].Usage.TotalTokens != 105 {
		t.Fatalf("compaction usage = %+v", events[1].Usage)
	}
}

func TestEngineRejectsOnDemandCompactionWithoutContext(t *testing.T) {
	provider := &compactingProvider{
		compact: func(context.Context, Request) (CompactResponse, error) {
			t.Fatal("unexpected compaction")
			return CompactResponse{}, nil
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if err := engine.Compact(context.Background(), discardEvents); err == nil {
		t.Fatalf("Compact() error = %v", err)
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
