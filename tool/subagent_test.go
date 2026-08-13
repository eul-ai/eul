package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func TestSubagentWaitDefaultTimeout(t *testing.T) {
	if defaultWaitTimeout != 30*time.Second || defaultWaitTimeoutMS != 30_000 {
		t.Fatalf("default wait timeout = %s (%dms)", defaultWaitTimeout, defaultWaitTimeoutMS)
	}
}

func TestSubagentWaitPresentationShowsProvidedTimeout(t *testing.T) {
	wait := &waitTool{}

	defaultPresentation := wait.Presentation(PresentationSnapshot{Arguments: map[string]any{"timeout_ms": nil}})
	if defaultPresentation.Arguments != "" {
		t.Fatalf("default presentation = %+v", defaultPresentation)
	}

	customPresentation := wait.Presentation(PresentationSnapshot{Arguments: map[string]any{"timeout_ms": json.Number("1500")}})
	if customPresentation.Arguments != "(1.5s timeout)" {
		t.Fatalf("custom presentation = %+v", customPresentation)
	}
}

func TestSubagentWaitIsSynchronizationOnly(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(context.Context, subagent.RunRequest, func(subagent.Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "research result"}, nil
	})})
	defer manager.Close()
	launch := NewSubagent(manager)
	wait := NewSubagentWait(manager)

	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	result, err := wait.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "completion is available") || strings.Contains(result.Output, "research result") {
		t.Fatalf("wait result = %+v, error = %v", result, err)
	}
	if len(manager.SnapshotInbox().MessageIDs) != 1 {
		t.Fatal("wait drained inbox")
	}
}

func TestSubagentWaitSteeringResultPreservesOriginalTask(t *testing.T) {
	message := waitResultMessage(subagent.WaitSteering)
	if !strings.Contains(message, "continue the original task") || !strings.Contains(message, "call subagent_wait again") {
		t.Fatalf("steering result = %q", message)
	}
}

func TestSubagentWaitTimesOutWithoutCancelingChild(t *testing.T) {
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
	if _, err := manager.Start([]subagent.Task{{Description: "inspect", Prompt: "inspect"}}, subagent.ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	wait := NewSubagentWait(manager)

	var presentation agent.ToolPresentation
	updates := toolUpdateSinkFunc(func(update agent.ToolPresentation) error {
		presentation = update
		return nil
	})
	result, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1}`), updates)
	if err != nil || result.IsError || !strings.Contains(result.Output, "No subagent completion") || presentation.Arguments != "(1ms timeout)" {
		t.Fatalf("wait result = %+v, presentation = %+v, error = %v", result, presentation, err)
	}
	select {
	case <-childDone:
		t.Fatal("timed out wait canceled child")
	default:
	}

	close(release)
	result, err = wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1000}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "completion is available") {
		t.Fatalf("completion wait result = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitValidatesTimeout(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{})
	defer manager.Close()
	wait := NewSubagentWait(manager)

	for _, arguments := range []string{
		`{"timeout_ms":0}`,
		`{"timeout_ms":-1}`,
		`{"timeout_ms":3600001}`,
		`{"other":1}`,
	} {
		result, err := wait.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
}
