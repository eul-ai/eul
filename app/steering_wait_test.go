package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

type steeringWaitProvider func(context.Context, agent.Request) (agent.Response, error)

func (provider steeringWaitProvider) Generate(ctx context.Context, request agent.Request, _ agent.StreamObserver) (agent.Response, error) {
	return provider(ctx, request)
}

func TestSubagentWaitIsInterruptedBySteeringWithoutCancelingChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(ctx context.Context, _ subagent.RunRequest, _ func(subagent.Progress)) (agent.RunResult, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-release:
			return agent.RunResult{Text: "done"}, nil
		}
	})})
	defer manager.Close()

	registry, err := tool.NewRegistry([]tool.Tool{tool.NewSubagent(manager), tool.NewSubagentWait(manager)})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := steeringWaitProvider(func(_ context.Context, request agent.Request) (agent.Response, error) {
		calls++
		switch calls {
		case 1:
			return agent.Response{ToolCalls: []agent.ToolCall{{
				ID:        "launch",
				Name:      "subagent",
				Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`),
			}}}, nil
		case 2:
			return agent.Response{ToolCalls: []agent.ToolCall{{
				ID:        "wait",
				Name:      "subagent_wait",
				Arguments: json.RawMessage(`{"timeout_ms":1000}`),
			}}}, nil
		case 3:
			if len(request.Inputs) != 2 || request.Inputs[0].Kind != agent.InputToolResult || !strings.Contains(request.Inputs[0].PlainText(), "then continue the original task") || request.Inputs[1].Kind != agent.InputUser || request.Inputs[1].PlainText() != "redirect" || !strings.Contains(request.Instructions, "Do not finish while required delegated work is still active") {
				return agent.Response{}, fmt.Errorf("steering request = %+v", request)
			}
			return agent.Response{Text: "redirected"}, nil
		default:
			return agent.Response{}, fmt.Errorf("unexpected provider call %d", calls)
		}
	})
	engine := agent.New(provider, registry, agent.Options{Model: "model", Inbox: manager, AdditionalInstructions: subagentInstructions(manager)})

	waitStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := engine.Run(context.Background(), "start", func(event agent.Event) error {
			if event.Kind == agent.EventToolStart && event.Call.Name == "subagent_wait" {
				close(waitStarted)
			}
			return nil
		})
		done <- err
	}()
	<-waitStarted
	if !engine.Steer("redirect") {
		t.Fatal("active engine rejected steering")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-childDone:
		t.Fatal("steering interruption canceled child")
	default:
	}

	close(release)
	<-childDone
}
