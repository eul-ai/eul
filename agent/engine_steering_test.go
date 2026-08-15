package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
)

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
	if !engine.Steer(textParts("steer one")) || !engine.Steer(textParts("steer two")) {
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
			delivered = append(delivered, NewUserInput(event.Content...).PlainText())
		}
	}
	if !slices.Equal(delivered, []string{"steer one", "steer two"}) {
		t.Fatalf("delivered steering = %q", delivered)
	}
	if engine.Steer(textParts("too late")) {
		t.Fatal("completed engine accepted steering")
	}
}

func TestEnginePreservesImageSteeringContent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	want := []ContentPart{
		{Kind: ContentPartText, Text: "describe "},
		{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: ContentPartText, Text: " please"},
	}
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			assertUserInput(t, request, "start")
			close(started)
			<-release
			return Response{Text: "initial", State: []byte("one")}, nil
		},
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || !reflect.DeepEqual(request.Inputs[0], NewUserInput(want...)) {
				t.Fatalf("image steering input = %+v", request.Inputs)
			}
			return Response{Text: "done", State: []byte("two")}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{})
	done := make(chan error, 1)
	var delivered []ContentPart
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event Event) error {
			if event.Kind == EventSteering {
				delivered = cloneContentParts(event.Content)
			}
			return nil
		})
		done <- err
	}()

	<-started
	steering := cloneContentParts(want)
	if !engine.Steer(steering) {
		t.Fatal("active engine rejected image steering")
	}
	steering[0].Text = "changed"
	steering[1].Image.Data[0] = 'x'
	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(delivered, want) {
		t.Fatalf("steering event content = %+v", delivered)
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
				NewTextInput("redirect"),
			}
			if string(request.State) != "tools" || !reflect.DeepEqual(request.Inputs, want) {
				t.Fatalf("steered tool continuation = %+v", request)
			}
			return Response{Text: "redirected", State: []byte("done")}, nil
		},
	}}
	executions := make(chan struct{}, 2)
	var startedOnce sync.Once
	toolbox := &fakeToolbox{execute: func(_ context.Context, call ToolCall) (ToolResult, error) {
		executions <- struct{}{}
		startedOnce.Do(func() { close(toolStarted) })
		<-releaseTool
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
	if !engine.Steer(textParts("redirect")) {
		t.Fatal("engine rejected steering during tool execution")
	}
	close(releaseTool)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executions) != 2 {
		t.Fatalf("tool executions = %d", len(executions))
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
	if !engine.Steer(textParts("steer")) {
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
				NewTextInput("steer"),
			}
			if string(request.State) != "tools" || !reflect.DeepEqual(request.Inputs, want) {
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
			queued = engine.Steer(textParts("steer"))
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
	if !engine.Steer(textParts("queued")) {
		t.Fatal("active engine rejected steering")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("stale steering = %+v", queued)
	}
	if engine.Steer(textParts("late")) {
		t.Fatal("canceled engine accepted steering")
	}
	if err := engine.Reset(); err != nil {
		t.Fatal(err)
	}
	if queued := engine.ClearSteering(); len(queued) != 0 {
		t.Fatalf("reset steering = %+v", queued)
	}
}
