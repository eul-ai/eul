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

type scriptedProvider struct {
	t     *testing.T
	steps []providerStep
	calls int
}

func (p *scriptedProvider) Generate(ctx context.Context, request Request, onText TextSink) (Response, error) {
	p.t.Helper()
	if p.calls >= len(p.steps) {
		p.t.Fatalf("unexpected provider call %d", p.calls+1)
	}
	step := p.steps[p.calls]
	p.calls++
	return step(ctx, request, onText)
}

type fakeToolbox struct {
	definitions []ToolDefinition
	execute     func(context.Context, ToolCall) (ToolResult, error)
}

func (t *fakeToolbox) Definitions() []ToolDefinition {
	return slices.Clone(t.definitions)
}

func (t *fakeToolbox) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	if t.execute == nil {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	return t.execute(ctx, call)
}

func TestEngineRunsToolLoopAndCarriesProviderState(t *testing.T) {
	provider := &scriptedProvider{t: t}
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
			{Name: "write", PromptSummary: "Create or overwrite files"},
			{Name: "read", PromptSummary: "Read file contents"},
		},
		execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
			if call.ID != "call-1" || call.Name != "read" || string(call.Arguments) != `{"path":"README.md"}` {
				t.Fatalf("unexpected tool call: %+v", call)
			}
			return ToolResult{Output: "file contents"}, nil
		},
	}

	engine := newTestEngine(t, provider, toolbox, Options{Model: "test-model", MaxToolRounds: 2})
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
	if engine.NeedsReset() {
		t.Fatal("completed turn unexpectedly requires reset")
	}
	if result.Usage != (Usage{InputTokens: 18, OutputTokens: 5, TotalTokens: 23}) {
		t.Fatalf("usage = %+v", result.Usage)
	}
	wantKinds := []EventKind{EventAssistantText, EventToolStart, EventToolEnd, EventAssistantText}
	if got := eventKinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", got, wantKinds)
	}
	if events[0].Text != "Checking" || events[1].Call.ID != "call-1" || events[2].Result.CallID != "call-1" || events[3].Text != " done" {
		t.Fatalf("unexpected event payloads: %+v", events)
	}

	next, err := engine.Run(context.Background(), "next question", nil)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if next.Text != "next answer" {
		t.Fatalf("second result text = %q", next.Text)
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

	engine := newTestEngine(t, provider, toolbox, Options{MaxToolRounds: 1})
	if _, err := engine.Run(context.Background(), "run both", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !slices.Equal(executionOrder, []string{"second", "first"}) {
		t.Fatalf("execution order = %v", executionOrder)
	}
}

func TestEngineReturnsUnknownToolsAndMalformedArgumentsToProvider(t *testing.T) {
	toolExecutions := 0
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{
				ToolCalls: []ToolCall{
					{ID: "bad-json", Name: "read", Arguments: json.RawMessage(`{"path":`)},
					{ID: "unknown", Name: "missing", Arguments: json.RawMessage(`{}`)},
				},
				State: []byte("calls"),
			}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 2 {
				t.Fatalf("error result count = %d, want 2", len(request.Inputs))
			}
			for _, input := range request.Inputs {
				if !input.IsError {
					t.Fatalf("input is not marked as error: %+v", input)
				}
			}
			if request.Inputs[0].CallID != "bad-json" || !strings.Contains(request.Inputs[0].Text, "malformed JSON") {
				t.Fatalf("malformed result = %+v", request.Inputs[0])
			}
			if request.Inputs[1].CallID != "unknown" || !strings.Contains(request.Inputs[1].Text, "unknown tool") {
				t.Fatalf("unknown result = %+v", request.Inputs[1])
			}
			return Response{Text: "recovered", State: []byte("done")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		toolExecutions++
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}}

	engine := newTestEngine(t, provider, toolbox, Options{MaxToolRounds: 1})
	result, err := engine.Run(context.Background(), "recover", nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("result text = %q", result.Text)
	}
	if toolExecutions != 1 {
		t.Fatalf("tool executions = %d, want 1; malformed JSON should not reach toolbox", toolExecutions)
	}
}

func TestEngineStopsAtToolRoundLimit(t *testing.T) {
	toolExecutions := 0
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "one", Name: "tool", Arguments: json.RawMessage(`{}`)}}, State: []byte("one")}, nil
		},
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "two", Name: "tool", Arguments: json.RawMessage(`{}`)}}, State: []byte("two")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(_ context.Context, _ ToolCall) (ToolResult, error) {
		toolExecutions++
		return ToolResult{Output: "ok"}, nil
	}}

	engine := newTestEngine(t, provider, toolbox, Options{MaxToolRounds: 1})
	_, err := engine.Run(context.Background(), "loop", nil)
	if !errors.Is(err, ErrToolRoundLimit) {
		t.Fatalf("Run() error = %v, want ErrToolRoundLimit", err)
	}
	if toolExecutions != 1 {
		t.Fatalf("tool executions = %d, want 1", toolExecutions)
	}
	if _, nextErr := engine.Run(context.Background(), "continue", nil); !errors.Is(nextErr, ErrResetRequired) {
		t.Fatalf("Run() after incomplete tool turn error = %v, want ErrResetRequired", nextErr)
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
			if len(request.State) != 0 {
				t.Fatalf("state after required reset = %q, want empty", request.State)
			}
			return Response{Text: "recovered", State: []byte("recovered")}, nil
		},
	}}
	toolbox := &fakeToolbox{execute: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		once.Do(func() { close(toolStarted) })
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	}}
	engine := newTestEngine(t, provider, toolbox, Options{MaxToolRounds: 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, "wait", nil)
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

	if !engine.NeedsReset() {
		t.Fatal("canceled tool turn does not report required reset")
	}
	if _, err := engine.Run(context.Background(), "continue", nil); !errors.Is(err, ErrResetRequired) {
		t.Fatalf("Run() after canceled tool error = %v, want ErrResetRequired", err)
	}
	engine.Reset()
	if engine.NeedsReset() {
		t.Fatal("Reset() did not clear required reset state")
	}
	result, err := engine.Run(context.Background(), "after reset", nil)
	if err != nil {
		t.Fatalf("Run() after Reset() error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("Run() after Reset() text = %q", result.Text)
	}
}

func TestEngineHonorsCancellationWhileQueued(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, _ Request, _ TextSink) (Response, error) {
			close(providerStarted)
			<-releaseProvider
			return Response{Text: "done", State: []byte("done")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "first", nil)
		firstDone <- err
	}()
	select {
	case <-providerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queuedDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(ctx, "queued", nil)
		queuedDone <- err
	}()
	select {
	case err := <-queuedDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled queued Run() remained blocked")
	}

	close(releaseProvider)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Run() error = %v", err)
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
			if len(request.State) != 0 {
				t.Fatalf("state after reset = %q, want empty", request.State)
			}
			return Response{Text: "second"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	if _, err := engine.Run(context.Background(), "first", nil); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	engine.Reset()
	if _, err := engine.Run(context.Background(), "second", nil); err != nil {
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
	engine := newTestEngine(t, provider, toolbox, Options{MaxToolRounds: 1})

	result, err := engine.Run(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("result text = %q", result.Text)
	}
}

func TestEngineReturnsCanceledContextBeforeAcquiringAvailableGate(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(context.Context, Request, TextSink) (Response, error) {
			t.Fatal("provider called for an already canceled run")
			return Response{}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := engine.Run(ctx, "canceled", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestEngineDefensivelyCopiesNestedToolSchemas(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			schema := &request.Tools[0].Parameters
			schema.Required[0] = "changed"
			schema.AnyOf[0].Type = "number"
			property := schema.Properties["items"]
			property.Items.Type = "number"
			schema.Properties["items"] = property
			return Response{Text: "first", State: []byte("first")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			schema := request.Tools[0].Parameters
			if !slices.Equal(schema.Required, []string{"items"}) {
				t.Fatalf("required fields were mutated: %v", schema.Required)
			}
			if schema.AnyOf[0].Type != "object" {
				t.Fatalf("AnyOf was mutated: %+v", schema.AnyOf)
			}
			if schema.Properties["items"].Items.Type != "string" {
				t.Fatalf("Items was mutated: %+v", schema.Properties["items"])
			}
			return Response{Text: "second", State: []byte("second")}, nil
		},
	}}
	items := JSONSchema{Type: "array", Items: &JSONSchema{Type: "string"}}
	toolbox := &fakeToolbox{definitions: []ToolDefinition{{
		Name: "read",
		Parameters: JSONSchema{
			Type:       "object",
			Required:   []string{"items"},
			Properties: map[string]JSONSchema{"items": items},
			AnyOf:      []JSONSchema{{Type: "object"}},
		},
	}}}
	engine := newTestEngine(t, provider, toolbox, Options{})

	if _, err := engine.Run(context.Background(), "first", nil); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, err := engine.Run(context.Background(), "second", nil); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestNewRejectsDuplicateToolDefinitions(t *testing.T) {
	provider := &scriptedProvider{t: t}
	toolbox := &fakeToolbox{definitions: []ToolDefinition{{Name: "read"}, {Name: "read"}}}

	if _, err := New(provider, toolbox, Options{}); err == nil || !strings.Contains(err.Error(), "duplicate tool definition") {
		t.Fatalf("New() error = %v, want duplicate tool definition error", err)
	}
}

func newTestEngine(t *testing.T, provider Provider, toolbox Toolbox, options Options) *Engine {
	t.Helper()
	engine, err := New(provider, toolbox, options)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return engine
}

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
