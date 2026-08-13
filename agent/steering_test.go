package agent

import (
	"context"
	"fmt"
	"testing"
)

type steeringWaitToolbox struct {
	fakeToolbox
	started chan struct{}
}

func (toolbox *steeringWaitToolbox) Execute(ctx context.Context, call ToolCall, _ ToolUpdateSink) (ToolResult, error) {
	close(toolbox.started)
	select {
	case <-ctx.Done():
		return ToolResult{}, ctx.Err()
	case <-SteeringSignal(ctx):
		return ToolResult{CallID: call.ID, Tool: call.Name, Output: "wait interrupted"}, nil
	}
}

func TestSteeringSignalInterruptsToolWaitAndDeliversContinuation(t *testing.T) {
	toolbox := &steeringWaitToolbox{started: make(chan struct{})}
	toolbox.definitions = []ToolDefinition{{Name: "wait"}}

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
		_, err := engine.Run(context.Background(), "start", discardEvents)
		done <- err
	}()
	<-toolbox.started
	if !engine.Steer("redirect") {
		t.Fatal("active engine rejected steering")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
