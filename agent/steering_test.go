package agent

import (
	"context"
	"fmt"
	"testing"
)

func TestSteeringSignalInterruptsToolWaitAndDeliversContinuation(t *testing.T) {
	started := make(chan struct{})
	toolbox := &fakeToolbox{definitions: toolDefinitions("wait"), execute: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		case <-SteeringSignal(ctx):
			return ToolResult{Output: "wait interrupted"}, nil
		}
	}}

	calls := 0
	provider := streamingProviderFunc(func(_ context.Context, request Request, _ TextSink, _ TextSink, _ ToolCallSink) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "wait", Name: "wait", Arguments: []byte(`{}`)}}}, nil
		case 2:
			if len(request.Inputs) != 2 || request.Inputs[0].Kind != InputToolResult || request.Inputs[1].Kind != InputUser || request.Inputs[1].PlainText() != "redirect" {
				return Response{}, fmt.Errorf("steering inputs = %+v", request.Inputs)
			}
			return Response{Text: "redirected"}, nil
		default:
			return Response{}, fmt.Errorf("unexpected provider call %d", calls)
		}
	})
	engine := newTestEngine(t, provider, toolbox, Options{})

	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), textParts("start"), discardEvents)
		done <- err
	}()
	<-started
	if !engine.Steer(textParts("redirect")) {
		t.Fatal("active engine rejected steering")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
