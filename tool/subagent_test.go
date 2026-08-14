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

func TestSubagentLaunchUsesPerTaskPolicyDefaultsAndSurfacesManagerValidation(t *testing.T) {
	requests := make(chan subagent.RunRequest, 2)
	manager := subagent.NewManager(subagent.Config{
		Runner: subagent.RunFunc(func(_ context.Context, request subagent.RunRequest, _ func(subagent.Progress)) (agent.RunResult, error) {
			requests <- request
			return agent.RunResult{}, nil
		}),
		SupportedThinkingLevels: func(subagent.Profile) []agent.ThinkingLevel {
			return []agent.ThinkingLevel{agent.ThinkingMedium}
		},
	})
	defer manager.Close()
	launch := NewSubagent(manager)

	result, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"default","prompt":"default"},{"description":"custom","prompt":"custom","model_profile":"fast","thinking_level":"medium"}]}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	for range 2 {
		request := <-requests
		if request.Task == "default" && (request.Profile != subagent.ProfileBalanced || request.ThinkingLevel != agent.ThinkingMedium) {
			t.Fatalf("default request = %+v", request)
		}
		if request.Task == "custom" && (request.Profile != subagent.ProfileFast || request.ThinkingLevel != agent.ThinkingMedium) {
			t.Fatalf("custom request = %+v", request)
		}
	}

	for _, arguments := range []string{
		`{"tasks":[]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"unknown"}]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect","thinking_level":"max"}]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect"}],"model_profile":"fast"}`,
	} {
		result, err := launch.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
}

func TestSubagentLaunchSchemaPlacesPolicyOnTasks(t *testing.T) {
	definition := NewSubagent(nil).Definition()
	if _, ok := definition.Parameters.Properties["model_profile"]; ok {
		t.Fatal("launch-level model profile remains in schema")
	}
	if _, ok := definition.Parameters.Properties["thinking_level"]; ok {
		t.Fatal("launch-level thinking level remains in schema")
	}

	task := definition.Parameters.Properties["tasks"].Items
	if task == nil || task.Properties["model_profile"].Type != "string" || task.Properties["thinking_level"].Type != "string" {
		t.Fatalf("task schema = %+v", task)
	}
	thinkingDescription := task.Properties["thinking_level"].Description
	for _, level := range subagent.AllowedThinkingLevels() {
		if !strings.Contains(thinkingDescription, string(level)) {
			t.Fatalf("thinking-level description %q does not include %q", thinkingDescription, level)
		}
	}
	if !strings.Contains(thinkingDescription, string(subagent.DefaultThinkingLevel)+" (default)") {
		t.Fatalf("thinking-level description = %q", thinkingDescription)
	}
}

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
	if len(manager.Snapshot().PendingCompletions) != 1 {
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
	if _, err := manager.Start([]subagent.Task{{Description: "inspect", Prompt: "inspect"}}); err != nil {
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
