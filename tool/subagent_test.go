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

func TestSubagentLaunchUsesPerTaskPolicyDefaultsAndValidation(t *testing.T) {
	requests := make(chan subagent.Task, 2)
	manager := subagent.NewManager(func(_ context.Context, task subagent.Task, _ func(agent.Usage)) (string, error) {
		requests <- task
		return "", nil
	})
	defer manager.Close()
	launch := NewLaunchSubagents(manager, uniformThinkingLevels(agent.ThinkingMedium), agent.NewSettings(agent.ThinkingHigh, false))

	result, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"default","prompt":"default"},{"description":"custom","prompt":"custom","model_profile":"fast","thinking_level":"medium"}]}`), discardUpdates)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	for range 2 {
		task := <-requests
		if task.Prompt == "default" && (task.ModelProfile != subagent.ProfileMain || task.ThinkingLevel != agent.ThinkingMedium) {
			t.Fatalf("default task = %+v", task)
		}
		if task.Prompt == "custom" && (task.ModelProfile != subagent.ProfileFast || task.ThinkingLevel != agent.ThinkingMedium) {
			t.Fatalf("custom task = %+v", task)
		}
	}

	for _, arguments := range []string{
		`{"tasks":[]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"unknown"}]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect","thinking_level":"max"}]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect","thinking_level":"invalid"}]}`,
		`{"tasks":[{"description":"inspect","prompt":"inspect"}],"model_profile":"fast"}`,
	} {
		result, err := launch.Execute(context.Background(), json.RawMessage(arguments), discardUpdates)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
}

func TestSubagentLaunchSchemaPlacesPolicyOnTasks(t *testing.T) {
	definition := NewLaunchSubagents(nil, uniformThinkingLevels(agent.ThinkingMedium), nil).Definition()
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
	definition := NewLaunchSubagents(nil, map[subagent.Profile][]agent.ThinkingLevel{
		subagent.ProfileFast:     {agent.ThinkingLow},
		subagent.ProfileBalanced: {agent.ThinkingMedium},
		subagent.ProfileMain:     {agent.ThinkingHigh},
	}, nil).Definition()
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
	launch := NewLaunchSubagents(nil, uniformThinkingLevels(agent.ThinkingMedium), nil)

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
	initial := launch.Presentation(`{"tasks":[{}]}`)
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
	wait := NewWaitForSubagent(nil)

	defaultPresentation := wait.Presentation(`{"timeout_ms":null}`)
	if defaultPresentation.Arguments != "" {
		t.Fatalf("default presentation = %+v", defaultPresentation)
	}

	customPresentation := wait.Presentation(`{"timeout_ms":1500}`)
	if !strings.Contains(customPresentation.Arguments, (1500 * time.Millisecond).String()) {
		t.Fatalf("custom presentation = %+v", customPresentation)
	}
}

func TestSubagentWaitIsSynchronizationOnly(t *testing.T) {
	manager := subagent.NewManager(func(context.Context, subagent.Task, func(agent.Usage)) (string, error) {
		return "research result", nil
	})
	defer manager.Close()
	launch := NewLaunchSubagents(manager, uniformThinkingLevels(agent.ThinkingMedium), agent.NewSettings(agent.ThinkingMedium, false))
	wait := NewWaitForSubagent(manager)

	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`), discardUpdates); err != nil {
		t.Fatal(err)
	}
	result, err := wait.Execute(context.Background(), json.RawMessage(`{}`), discardUpdates)
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
	manager := subagent.NewManager(func(ctx context.Context, _ subagent.Task, _ func(agent.Usage)) (string, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "done", nil
		}
	})
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
	result, err = wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1000}`), discardUpdates)
	if err != nil || result.IsError {
		t.Fatalf("completion wait result = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitValidatesTimeout(t *testing.T) {
	wait := NewWaitForSubagent(nil)

	for _, arguments := range []string{
		`{"timeout_ms":0}`,
		`{"timeout_ms":-1}`,
		`{"timeout_ms":3600001}`,
		`{"other":1}`,
	} {
		result, err := wait.Execute(context.Background(), json.RawMessage(arguments), discardUpdates)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
}

func uniformThinkingLevels(levels ...agent.ThinkingLevel) map[subagent.Profile][]agent.ThinkingLevel {
	return map[subagent.Profile][]agent.ThinkingLevel{
		subagent.ProfileFast:     levels,
		subagent.ProfileBalanced: levels,
		subagent.ProfileMain:     levels,
	}
}
