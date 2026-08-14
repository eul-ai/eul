package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEngineRetriesGenerationBeforeObservableEvents(t *testing.T) {
	transient := errors.New("temporary provider failure")
	generateCalls := 0
	provider := &retryingProvider{
		generate: func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
			generateCalls++
			switch generateCalls {
			case 1:
				assertUserInput(t, request, "write the file")
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
			t.Fatalf("unexpected call: %+v", call)
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
		EventGenerationRetry,
		EventContextUsage, EventToolStart, EventToolExecute, EventToolEnd,
		EventContextUsage,
	}) {
		t.Fatalf("events = %v", got)
	}
	if events[0].Attempt != 2 {
		t.Fatalf("retry event = %+v", events[0])
	}
}

func TestEngineRetriesGenerationAfterObservableEvent(t *testing.T) {
	transient := errors.New("temporary provider failure")
	tests := []struct {
		name string
		emit func(TextSink, TextSink, ToolCallSink) error
		want []EventKind
	}{
		{
			name: "text",
			emit: func(onText TextSink, _ TextSink, _ ToolCallSink) error { return onText("partial") },
			want: []EventKind{EventAssistantText, EventGenerationRetry, EventContextUsage},
		},
		{
			name: "reasoning",
			emit: func(_ TextSink, onReasoning TextSink, _ ToolCallSink) error { return onReasoning("thinking") },
			want: []EventKind{EventAssistantReasoning, EventGenerationRetry, EventContextUsage},
		},
		{
			name: "tool presentation",
			emit: func(_ TextSink, _ TextSink, onToolCall ToolCallSink) error {
				return onToolCall(ToolCallSnapshot{ID: "call-1", Name: "write", RawArguments: `{"path":"demo.txt"}`})
			},
			want: []EventKind{EventToolStart, EventToolEnd, EventGenerationRetry, EventContextUsage},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generateCalls := 0
			retryCalls := 0
			provider := &retryingProvider{
				generate: func(_ context.Context, _ Request, onText TextSink, onReasoning TextSink, onToolCall ToolCallSink) (Response, error) {
					generateCalls++
					if generateCalls == 2 {
						return Response{Text: "done"}, nil
					}
					if err := test.emit(onText, onReasoning, onToolCall); err != nil {
						return Response{}, err
					}
					return Response{}, transient
				},
				retry: func(error, int) (time.Duration, bool) {
					retryCalls++
					return 0, true
				},
			}
			var events []Event
			engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
			result, err := engine.Run(context.Background(), "start", func(event Event) error {
				events = append(events, event)
				return nil
			})
			if err != nil || result.Text != "done" || generateCalls != 2 || retryCalls != 1 {
				t.Fatalf("result = %+v, generate calls = %d, retry calls = %d, error = %v", result, generateCalls, retryCalls, err)
			}
			if got := eventKinds(events); !slices.Equal(got, test.want) {
				t.Fatalf("events = %v, want %v", got, test.want)
			}
			if test.name == "tool presentation" && (!events[1].Result.IsError || !strings.Contains(events[1].Result.Output, transient.Error())) {
				t.Fatalf("closed tool result = %+v", events[1].Result)
			}
		})
	}
}

func TestEngineDoesNotRetryAfterVisibleSinkFailure(t *testing.T) {
	sinkErr := errors.New("sink failed")
	generateCalls := 0
	retryCalls := 0
	provider := &retryingProvider{
		generate: func(_ context.Context, _ Request, onText TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
			generateCalls++
			if err := onText("partial"); err != nil {
				return Response{}, err
			}
			return Response{}, errors.New("provider ignored sink failure")
		},
		retry: func(error, int) (time.Duration, bool) {
			retryCalls++
			return 0, true
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventAssistantText {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) || generateCalls != 1 || retryCalls != 0 {
		t.Fatalf("generate calls = %d, retry calls = %d, error = %v", generateCalls, retryCalls, err)
	}
}

func TestEngineDoesNotCompactAfterRetryEventSinkFailure(t *testing.T) {
	transient := errors.New("temporary provider failure")
	sinkErr := errors.New("sink failed")
	policyCalls := 0
	compactCalls := 0
	provider := &retryingCompactingProvider{
		retryingProvider: &retryingProvider{
			generate: func(context.Context, Request, TextSink, TextSink, ToolCallSink) (Response, error) {
				return Response{}, transient
			},
			retry: func(error, int) (time.Duration, bool) { return 0, true },
		},
		shouldCompactAfterError: func(Request, error) bool {
			policyCalls++
			return true
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			compactCalls++
			return CompactResponse{State: []byte("compacted")}, nil
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	_, err := engine.Run(context.Background(), "start", func(event Event) error {
		if event.Kind == EventGenerationRetry {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) || policyCalls != 0 || compactCalls != 0 {
		t.Fatalf("policy calls = %d, compact calls = %d, error = %v", policyCalls, compactCalls, err)
	}
}

func TestEngineDoesNotCompactAfterObservableGenerationError(t *testing.T) {
	contextLimit := errors.New("context limit exceeded")
	generateCalls := 0
	provider := streamingProviderFunc(func(_ context.Context, _ Request, _ TextSink, onReasoning TextSink, _ ToolCallSink) (Response, error) {
		generateCalls++
		if err := onReasoning("partial reasoning"); err != nil {
			return Response{}, err
		}
		return Response{}, contextLimit
	})
	policyCalls := 0
	compactCalls := 0
	compacting := &compactingProvider{
		Provider: provider,
		shouldCompactAfterError: func(Request, error) bool {
			policyCalls++
			return true
		},
		compact: func(context.Context, Request) (CompactResponse, error) {
			compactCalls++
			return CompactResponse{State: []byte("compacted")}, nil
		},
	}
	engine := newTestEngine(t, compacting, &fakeToolbox{}, Options{})
	_, err := engine.Run(context.Background(), "start", discardEvents)
	if !errors.Is(err, contextLimit) || generateCalls != 1 || policyCalls != 0 || compactCalls != 0 {
		t.Fatalf("generate calls = %d, policy calls = %d, compact calls = %d, error = %v", generateCalls, policyCalls, compactCalls, err)
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
