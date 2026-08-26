package tool

import (
	"context"
	"encoding/json"
	"slices"
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
	launch := NewLaunchSubagents(manager)

	result, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"default","prompt":"default"},{"description":"custom","prompt":"custom","model_profile":"fast","thinking_level":"medium"}]}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	for range 2 {
		request := <-requests
		if request.Task == "default" && (request.Profile != subagent.ProfileMain || request.ThinkingLevel != agent.ThinkingMedium) {
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
	definition := NewLaunchSubagents(nil).Definition()
	if _, ok := definition.Parameters.Properties["model_profile"]; ok {
		t.Fatal("launch-level model profile remains in schema")
	}
	if _, ok := definition.Parameters.Properties["thinking_level"]; ok {
		t.Fatal("launch-level thinking level remains in schema")
	}

	task := definition.Parameters.Properties["tasks"].Items
	if task == nil || !slices.Equal(task.Required, []string{"description", "prompt", "model_profile", "thinking_level"}) {
		t.Fatalf("task schema = %+v", task)
	}
	modelProfile := task.Properties["model_profile"]
	thinkingLevel := task.Properties["thinking_level"]
	if !slices.Equal(modelProfile.Type.([]string), []string{"string", "null"}) || !slices.Equal(thinkingLevel.Type.([]string), []string{"string", "null"}) {
		t.Fatalf("task policy schema = %+v", task)
	}
}

func TestSubagentLaunchSchemaUsesProfileThinkingLevels(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{
		SupportedThinkingLevels: func(profile subagent.Profile) []agent.ThinkingLevel {
			switch profile {
			case subagent.ProfileFast:
				return []agent.ThinkingLevel{agent.ThinkingLow}
			case subagent.ProfileBalanced:
				return []agent.ThinkingLevel{agent.ThinkingMedium}
			default:
				return []agent.ThinkingLevel{agent.ThinkingHigh}
			}
		},
	})
	defer manager.Close()

	definition := NewLaunchSubagents(manager).Definition()
	description := definition.Parameters.Properties["tasks"].Items.Properties["thinking_level"].Description
	for _, supported := range []string{"fast: low", "balanced: medium", "main: high"} {
		if !strings.Contains(description, supported) {
			t.Fatalf("thinking level description does not include %q: %q", supported, description)
		}
	}
	if strings.Contains(description, string(agent.ThinkingMinimal)) {
		t.Fatalf("thinking level description includes unsupported minimal level: %q", description)
	}
}

func TestSubagentLaunchFinalizesFailedPresentation(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{
		SupportedThinkingLevels: func(subagent.Profile) []agent.ThinkingLevel {
			return []agent.ThinkingLevel{agent.ThinkingMedium}
		},
	})
	defer manager.Close()
	launch := NewLaunchSubagents(manager).(*launchTool)

	var final agent.ToolPresentation
	finalCalls := 0
	updates := toolUpdateSinkFunc(func(presentation agent.ToolPresentation) error {
		final = presentation
		finalCalls++
		return nil
	})
	result, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect","thinking_level":"minimal"}]}`), updates)
	if err != nil || !result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	if finalCalls != 1 {
		t.Fatalf("final presentation calls = %d", finalCalls)
	}
	initial := launch.Presentation(PresentationSnapshot{Arguments: map[string]any{"tasks": []any{map[string]any{}}}})
	if final.Equal(initial) {
		t.Fatal("failed launch retained its pending presentation")
	}
}

func TestSubagentWaitDefaultTimeout(t *testing.T) {
	if defaultWaitTimeout != 30*time.Second || defaultWaitTimeoutMS != 30_000 {
		t.Fatalf("default wait timeout = %s (%dms)", defaultWaitTimeout, defaultWaitTimeoutMS)
	}
	if !slices.Equal(waitDefinition.Parameters.Required, []string{"timeout_ms"}) {
		t.Fatalf("required wait arguments = %v", waitDefinition.Parameters.Required)
	}
}

func TestSubagentWaitPresentationShowsProvidedTimeout(t *testing.T) {
	wait := &waitTool{}

	defaultPresentation := wait.Presentation(PresentationSnapshot{Arguments: map[string]any{"timeout_ms": nil}})
	if defaultPresentation.Arguments != "" {
		t.Fatalf("default presentation = %+v", defaultPresentation)
	}

	customPresentation := wait.Presentation(PresentationSnapshot{Arguments: map[string]any{"timeout_ms": json.Number("1500")}})
	if !strings.Contains(customPresentation.Arguments, (1500 * time.Millisecond).String()) {
		t.Fatalf("custom presentation = %+v", customPresentation)
	}
}

func TestSubagentWaitIsSynchronizationOnly(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(context.Context, subagent.RunRequest, func(subagent.Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "research result"}, nil
	})})
	defer manager.Close()
	launch := NewLaunchSubagents(manager)
	wait := NewWaitForSubagent(manager)

	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	result, err := wait.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("wait result = %+v, error = %v", result, err)
	}
	if len(manager.Snapshot().PendingCompletions) != 1 {
		t.Fatal("wait drained inbox")
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
	wait := NewWaitForSubagent(manager)

	var presentation agent.ToolPresentation
	updates := toolUpdateSinkFunc(func(update agent.ToolPresentation) error {
		presentation = update
		return nil
	})
	result, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1}`), updates)
	if err != nil || result.IsError || !strings.Contains(presentation.Arguments, time.Millisecond.String()) {
		t.Fatalf("wait result = %+v, presentation = %+v, error = %v", result, presentation, err)
	}
	select {
	case <-childDone:
		t.Fatal("timed out wait canceled child")
	default:
	}

	close(release)
	result, err = wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1000}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("completion wait result = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitValidatesTimeout(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{})
	defer manager.Close()
	wait := NewWaitForSubagent(manager)

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
