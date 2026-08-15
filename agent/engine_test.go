package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

func textParts(text string) []ContentPart {
	return []ContentPart{{Kind: ContentPartText, Text: text}}
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
	shouldCompact           func(Request, Usage) bool
	shouldCompactAfterError func(Request, error) bool
	compact                 func(context.Context, Request) (CompactResponse, error)
}

func (p *compactingProvider) ShouldCompact(request Request, usage Usage) bool {
	return p.shouldCompact != nil && p.shouldCompact(request, usage)
}

func (p *compactingProvider) ShouldCompactAfterError(request Request, err error) bool {
	return p.shouldCompactAfterError != nil && p.shouldCompactAfterError(request, err)
}

func (p *compactingProvider) Compact(ctx context.Context, request Request) (CompactResponse, error) {
	return p.compact(ctx, request)
}

type retryingCompactingProvider struct {
	*retryingProvider
	shouldCompactAfterError func(Request, error) bool
	compact                 func(context.Context, Request) (CompactResponse, error)
}

func (p *retryingCompactingProvider) ShouldCompact(Request, Usage) bool { return false }

func (p *retryingCompactingProvider) ShouldCompactAfterError(request Request, err error) bool {
	return p.shouldCompactAfterError(request, err)
}

func (p *retryingCompactingProvider) Compact(ctx context.Context, request Request) (CompactResponse, error) {
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

type executionOnlyToolbox struct {
	definitions []ToolDefinition
	execute     func(context.Context, ToolCall) (ToolResult, error)
}

func (toolbox *executionOnlyToolbox) Definitions() []ToolDefinition {
	return slices.Clone(toolbox.definitions)
}

func (toolbox *executionOnlyToolbox) Execute(ctx context.Context, call ToolCall, _ ToolUpdateSink) (ToolResult, error) {
	return toolbox.execute(ctx, call)
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
		want := []Input{NewTextInput("start"), NewTextInput("next")}
		if !reflect.DeepEqual(request.Inputs, want) {
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
	if !engine.Steer(textParts("queued")) {
		t.Fatal("active engine rejected steering")
	}
	close(release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("Run() error = %v", err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("stale steering = %+v", queued)
	}
	if engine.Steer(textParts("late")) {
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
	if err := engine.Compact(context.Background(), discardEvents); !errors.Is(err, errEngineBusy) {
		t.Fatalf("concurrent Compact() error = %v", err)
	}
	if err := engine.SetThinkingLevel(ThinkingHigh); err != nil {
		t.Fatalf("concurrent SetThinkingLevel() error = %v", err)
	}
	if got := engine.currentThinkingLevel(); got != ThinkingHigh {
		t.Fatalf("concurrent thinking level = %q", got)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := engine.Reset(); err != nil {
		t.Fatalf("Reset() after Run = %v", err)
	}
}

func TestEngineUsesThinkingLevelChangedDuringRunForNextGeneration(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if request.ThinkingLevel != DefaultThinkingLevel {
				t.Fatalf("first thinking level = %q", request.ThinkingLevel)
			}
			close(started)
			<-release
			return Response{ToolCalls: []ToolCall{{ID: "tool", Name: "tool"}}}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if request.ThinkingLevel != ThinkingHigh {
				t.Fatalf("second thinking level = %q", request.ThinkingLevel)
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "first", discardEvents)
		done <- err
	}()
	<-started
	if err := engine.SetThinkingLevel(ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEngineUsesFastModeChangedDuringRunForNextGeneration(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if request.FastMode {
				t.Fatal("first generation used fast mode")
			}
			close(started)
			<-release
			return Response{ToolCalls: []ToolCall{{ID: "tool", Name: "tool"}}}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if !request.FastMode {
				t.Fatal("second generation did not use fast mode")
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})

	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "first", discardEvents)
		done <- err
	}()
	<-started
	engine.SetFastMode(true)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
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

func TestEngineRunContent(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Content == nil || len(request.Inputs[0].Content) != 3 {
				t.Fatalf("inputs = %+v", request.Inputs)
			}
			parts := request.Inputs[0].Content
			image := parts[1].Image
			if parts[0].Text != "describe " || image == nil || image.MediaType != "image/png" || string(image.Data) != "png" || parts[2].Text != " please" {
				t.Fatalf("input = %+v", request.Inputs[0])
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	image := &Image{MediaType: "image/png", Data: []byte("png")}

	if _, err := engine.RunContent(context.Background(), []ContentPart{
		{Kind: ContentPartText, Text: "describe "},
		{Kind: ContentPartImage, Image: image},
		{Kind: ContentPartText, Text: " please"},
	}, discardEvents); err != nil {
		t.Fatal(err)
	}
}

func TestEnginePreservesTextOnlyContent(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].PlainText() != "beforeafter" || len(request.Inputs[0].Content) != 2 {
				t.Fatalf("input = %+v", request.Inputs)
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	if _, err := engine.RunContent(context.Background(), []ContentPart{
		{Kind: ContentPartText, Text: "before"},
		{Kind: ContentPartText, Text: "after"},
	}, discardEvents); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRetryOwnsContent(t *testing.T) {
	attempt := 0
	provider := &retryingProvider{
		generate: func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
			parts := request.Inputs[0].Content
			if parts[0].Text != "before" || string(parts[1].Image.Data) != "png" {
				t.Fatalf("retry content = %+v", parts)
			}
			attempt++
			if attempt == 1 {
				parts[0].Text = "changed"
				parts[1].Image.Data[0] = 'X'
				return Response{}, errors.New("retry")
			}
			return Response{Text: "done"}, nil
		},
		retry: func(_ error, failedAttempts int) (time.Duration, bool) {
			return 0, failedAttempts == 1
		},
	}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	content := []ContentPart{
		{Kind: ContentPartText, Text: "before"},
		{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
	}

	if _, err := engine.RunContent(context.Background(), content, discardEvents); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRequestOwnsContent(t *testing.T) {
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			request.Inputs[0].Content[0].Text = "changed"
			request.Inputs[0].Content[1].Image.Data[0] = 'X'
			return Response{}, errors.New("failed")
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			parts := request.Inputs[0].Content
			if parts[0].Text != "before" || string(parts[1].Image.Data) != "png" {
				t.Fatalf("content was mutated through provider request: %+v", parts)
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	content := []ContentPart{
		{Kind: ContentPartText, Text: "before"},
		{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
	}

	if _, err := engine.RunContent(context.Background(), content, discardEvents); err == nil {
		t.Fatal("first run succeeded")
	}
	if _, err := engine.RunContent(context.Background(), content, discardEvents); err != nil {
		t.Fatal(err)
	}
}

func newTestEngine(t *testing.T, provider Provider, toolbox Toolbox, options Options) *Engine {
	t.Helper()
	return New(provider, toolbox, options)
}

func discardEvents(Event) error { return nil }

func assertUserInput(t *testing.T, request Request, text string) {
	t.Helper()
	if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser || request.Inputs[0].PlainText() != text {
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
