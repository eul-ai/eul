package tool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestSubagentDefinitionsAllowSelectiveBackgroundUse(t *testing.T) {
	subagents := NewSubagent(nil)
	defer subagents.Close()

	launch := subagents.Definition()
	if launch.Name != "subagent" || !strings.Contains(launch.Description, "Use selectively") || !strings.Contains(launch.Description, "follow-up work") || strings.Contains(launch.Description, "explicitly asks") {
		t.Fatalf("launch definition = %+v", launch)
	}
	if launch.Parameters.Properties["tasks"].Items == nil || launch.Parameters.Properties["tasks"].Items.Type != "string" {
		t.Fatalf("tasks schema = %+v", launch.Parameters.Properties["tasks"])
	}

	model := launch.Parameters.Properties["model_profile"]
	if model.Type != "string" || !strings.Contains(model.Description, "Defaults to balanced") {
		t.Fatalf("model schema = %+v", model)
	}
	thinking := launch.Parameters.Properties["thinking_level"]
	if thinking.Type != "string" || !strings.Contains(thinking.Description, "Defaults to low") {
		t.Fatalf("thinking schema = %+v", thinking)
	}

	wait := NewSubagentWait(subagents).Definition()
	if wait.Name != "subagent_wait" || !strings.Contains(wait.Description, "synthesize the findings") {
		t.Fatalf("wait definition = %+v", wait)
	}
	if wait.Parameters.Properties["ids"].Items == nil || wait.Parameters.Properties["ids"].Items.Type != "string" {
		t.Fatalf("IDs schema = %+v", wait.Parameters.Properties["ids"])
	}

	cancel := NewSubagentCancel(subagents).Definition()
	if cancel.Name != "subagent_cancel" || cancel.Parameters.Properties["ids"].Items == nil {
		t.Fatalf("cancel definition = %+v", cancel)
	}
}

func TestSubagentLaunchReturnsWhileTasksRun(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	subagents := NewSubagent(func(ctx context.Context, task string, modelProfile SubagentModelProfile, thinkingLevel agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		started <- task + ":" + string(modelProfile) + ":" + string(thinkingLevel)
		select {
		case <-release:
			return agent.RunResult{Text: "result for " + task}, nil
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		}
	})
	defer subagents.Close()

	updates := &recordingSubagentUpdates{}
	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["first","second"]}`), updates)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	if !strings.Contains(result.Output, "Started subagents (model: balanced, thinking: low)") || !strings.Contains(result.Output, "subagent-1: first") || !strings.Contains(result.Output, "subagent-2: second") {
		t.Fatalf("launch output = %q", result.Output)
	}
	if updates.final.Arguments != "(balanced, low)" || !slices.Equal(updates.final.Lines, []string{"Starting 2 subagent(s)."}) {
		t.Fatalf("final presentation = %+v", updates.final)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case task := <-started:
			seen[task] = true
		case <-time.After(2 * time.Second):
			t.Fatal("tasks did not start")
		}
	}
	if !seen["first:balanced:low"] || !seen["second:balanced:low"] {
		t.Fatalf("started tasks = %v", seen)
	}

	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Running: 2})
	close(release)
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 2})
}

func TestSubagentRunsFourJobsConcurrently(t *testing.T) {
	started := make(chan struct{}, maxSubagents)
	release := make(chan struct{})
	subagents := NewSubagent(func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error) {
		started <- struct{}{}
		<-release
		return agent.RunResult{Text: "done"}, nil
	})
	defer subagents.Close()

	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two","three","four"]}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	for range maxSubagents {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("four jobs did not run concurrently")
		}
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Running: maxSubagents})
	close(release)
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: maxSubagents})
}

func TestSubagentThinkingLevelValidationAndOutput(t *testing.T) {
	levels := make(chan agent.ThinkingLevel, 1)
	subagents := NewSubagent(func(_ context.Context, _ string, _ SubagentModelProfile, thinkingLevel agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		levels <- thinkingLevel
		return agent.RunResult{Text: "done"}, nil
	}, agent.ThinkingLow, agent.ThinkingHigh)
	defer subagents.Close()

	for _, arguments := range []string{
		`{"tasks":["inspect"],"thinking_level":"medium"}`,
		`{"tasks":["inspect"],"thinking_level":"xhigh"}`,
		`{"tasks":["inspect"],"thinking_level":"max"}`,
		`{"tasks":["inspect"],"thinking_level":"extreme"}`,
	} {
		result, err := subagents.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}

	updates := &recordingSubagentUpdates{}
	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"thinking_level":"high"}`), updates)
	if err != nil || result.IsError || !strings.Contains(result.Output, "thinking: high") || updates.final.Arguments != "(balanced, high)" {
		t.Fatalf("result = %+v, presentation = %+v, error = %v", result, updates.final, err)
	}
	if level := <-levels; level != agent.ThinkingHigh {
		t.Fatalf("thinking level = %q", level)
	}
}

func TestSubagentThinkingLevelsFollowModelProfile(t *testing.T) {
	started := make(chan string, 2)
	subagents := NewSubagentWithThinkingLevels(func(_ context.Context, _ string, profile SubagentModelProfile, thinkingLevel agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		started <- string(profile) + ":" + string(thinkingLevel)
		return agent.RunResult{Text: "done"}, nil
	}, func(profile SubagentModelProfile) []agent.ThinkingLevel {
		switch profile {
		case SubagentModelFast:
			return []agent.ThinkingLevel{agent.ThinkingOff}
		case SubagentModelBalanced:
			return []agent.ThinkingLevel{agent.ThinkingHigh}
		default:
			return []agent.ThinkingLevel{agent.ThinkingLow}
		}
	})
	defer subagents.Close()

	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"model_profile":"fast"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	if got := <-started; got != "fast:off" {
		t.Fatalf("fast launch = %q", got)
	}

	result, err = subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"model_profile":"balanced","thinking_level":"low"}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "balanced model") {
		t.Fatalf("unsupported result = %+v, error = %v", result, err)
	}
	result, err = subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"model_profile":"balanced","thinking_level":"high"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("balanced result = %+v, error = %v", result, err)
	}
	if got := <-started; got != "balanced:high" {
		t.Fatalf("balanced launch = %q", got)
	}
}

func TestSubagentModelProfileValidationAndOutput(t *testing.T) {
	profiles := make(chan SubagentModelProfile, 1)
	subagents := NewSubagent(func(_ context.Context, _ string, modelProfile SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		profiles <- modelProfile
		return agent.RunResult{Text: "done"}, nil
	})
	defer subagents.Close()

	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"model_profile":"cheap"}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "fast, balanced, or powerful") {
		t.Fatalf("invalid profile result = %+v, error = %v", result, err)
	}

	updates := &recordingSubagentUpdates{}
	result, err = subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"],"model_profile":"fast"}`), updates)
	if err != nil || result.IsError || !strings.Contains(result.Output, "model: fast") || updates.final.Arguments != "(fast, low)" {
		t.Fatalf("result = %+v, presentation = %+v, error = %v", result, updates.final, err)
	}
	if profile := <-profiles; profile != SubagentModelFast {
		t.Fatalf("model profile = %q", profile)
	}
}

func TestSubagentWaitReturnsRequestedOrderAndConsumesResults(t *testing.T) {
	releases := map[string]chan struct{}{
		"first":  make(chan struct{}),
		"second": make(chan struct{}),
	}
	subagents := NewSubagent(func(_ context.Context, task string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		<-releases[task]
		return agent.RunResult{Text: task + " result"}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	launch, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["first","second"]}`), nil)
	if err != nil || launch.IsError {
		t.Fatalf("launch = %+v, error = %v", launch, err)
	}

	done := make(chan struct {
		result agent.ToolResult
		err    error
	}, 1)
	go func() {
		result, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-2","subagent-1"]}`), nil)
		done <- struct {
			result agent.ToolResult
			err    error
		}{result: result, err: err}
	}()

	close(releases["second"])
	select {
	case <-done:
		t.Fatal("wait returned before every selected task completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releases["first"])

	completed := <-done
	if completed.err != nil || completed.result.IsError {
		t.Fatalf("wait result = %+v, error = %v", completed.result, completed.err)
	}
	if !strings.HasPrefix(completed.result.Output, subagentResultGuidance) {
		t.Fatalf("wait guidance missing from %q", completed.result.Output)
	}
	second := strings.Index(completed.result.Output, "Subagent subagent-2 (model: balanced, thinking: low):\nsecond result")
	first := strings.Index(completed.result.Output, "Subagent subagent-1 (model: balanced, thinking: low):\nfirst result")
	if second < 0 || first < 0 || second >= first {
		t.Fatalf("wait output = %q", completed.result.Output)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{})

	again, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1"]}`), nil)
	if err != nil || !again.IsError || !strings.Contains(again.Output, "unknown or expired") {
		t.Fatalf("second wait = %+v, error = %v", again, err)
	}
}

func TestSubagentCompletionReleasesContextAndRetainsResult(t *testing.T) {
	contexts := make(chan context.Context, 1)
	subagents := NewSubagent(func(ctx context.Context, _ string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		contexts <- ctx
		return agent.RunResult{Text: "done"}, nil
	})
	defer subagents.Close()

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	jobContext := <-contexts
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 1})
	select {
	case <-jobContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("completed job context was not released")
	}
	if !errors.Is(context.Cause(jobContext), context.Canceled) {
		t.Fatalf("context cause = %v", context.Cause(jobContext))
	}

	result, err := NewSubagentWait(subagents).Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1"]}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "done") {
		t.Fatalf("wait = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitAfterCompletionReturnsImmediately(t *testing.T) {
	subagents := NewSubagent(func(_ context.Context, task string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "done " + task}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 1})

	result, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1"]}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "done inspect") {
		t.Fatalf("wait = %+v, error = %v", result, err)
	}
}

func TestSubagentStatusPublishesLiveUsage(t *testing.T) {
	usagePublished := make(chan struct{})
	release := make(chan struct{})
	subagents := NewSubagent(func(_ context.Context, _ string, _ SubagentModelProfile, _ agent.ThinkingLevel, update func(SubagentProgress)) (agent.RunResult, error) {
		usage := agent.Usage{InputTokens: 300, OutputTokens: 21, TotalTokens: 321}
		update(SubagentProgress{Usage: usage, Generations: 7})
		close(usagePublished)
		<-release
		return agent.RunResult{Text: "done", Usage: usage}, nil
	})
	defer subagents.Close()

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	<-usagePublished

	deadline := time.After(2 * time.Second)
	for {
		select {
		case status := <-subagents.StatusUpdates():
			if len(status.Jobs) == 1 && status.Jobs[0].Usage.InputTokens == 300 && status.Jobs[0].Usage.OutputTokens == 21 && status.Jobs[0].Generations == 7 {
				close(release)
				return
			}
		case <-deadline:
			t.Fatal("live subagent usage was not published")
		}
	}
}

func TestSubagentWaitCancellationCancelsSelectedJob(t *testing.T) {
	started := make(chan struct{})
	subagents := NewSubagent(func(ctx context.Context, _ string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		close(started)
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	_, err := wait.Execute(ctx, json.RawMessage(`{"ids":["subagent-1"]}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 1})

	result, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, errSubagentCanceled.Error()) {
		t.Fatalf("later wait = %+v, error = %v", result, err)
	}
}

func TestSubagentCancelCancelsSelectedJobs(t *testing.T) {
	started := make(chan string, 2)
	subagents := NewSubagent(func(ctx context.Context, task string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		started <- task
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)
	cancel := NewSubagentCancel(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	<-started
	<-started

	result, err := cancel.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-2"]}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "subagent-2") {
		t.Fatalf("cancel = %+v, error = %v", result, err)
	}
	result, err = wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-2"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, errSubagentCanceled.Error()) {
		t.Fatalf("wait = %+v, error = %v", result, err)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Running: 1})
}

func TestSubagentCloseCancelsAndJoinsJobs(t *testing.T) {
	started := make(chan struct{}, 2)
	finished := make(chan error, 2)
	subagents := NewSubagent(func(ctx context.Context, _ string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		finished <- context.Cause(ctx)
		return agent.RunResult{}, ctx.Err()
	})

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	for range 2 {
		<-started
	}
	if err := subagents.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case cause := <-finished:
			if !errors.Is(cause, errSubagentSessionClosed) {
				t.Fatalf("cancellation cause = %v", cause)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker was not joined")
		}
	}

	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["three"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "closed") {
		t.Fatalf("launch after close = %+v, error = %v", result, err)
	}
}

func TestSubagentValidatesAndLimitsOutstandingJobsBeforeLaunching(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, maxSubagents)
	var calls atomic.Int64
	subagents := NewSubagent(func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return agent.RunResult{Text: "done"}, nil
	})
	defer subagents.Close()

	for _, arguments := range []string{
		`{"tasks":[]}`,
		`{"tasks":["one","two","three","four","five"]}`,
		`{"tasks":["one","  "]}`,
	} {
		result, err := subagents.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("callback calls = %d", calls.Load())
	}

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two","three"]}`), nil); err != nil || result.IsError {
		t.Fatalf("first launch = %+v, error = %v", result, err)
	}
	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["four","five"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "must not exceed 4") {
		t.Fatalf("over-limit launch = %+v, error = %v", result, err)
	}
	for range 3 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("launched callback did not start")
		}
	}
	if calls.Load() != 3 {
		close(release)
		t.Fatalf("callback calls = %d", calls.Load())
	}
	close(release)
}

func TestSubagentWaitFreesOnlyCollectedCapacity(t *testing.T) {
	subagents := NewSubagent(func(_ context.Context, task string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "done " + task}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two","three","four"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 4})

	result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["five"]}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("launch with uncollected results = %+v, error = %v", result, err)
	}
	result, err = wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1","subagent-3"]}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("partial wait = %+v, error = %v", result, err)
	}
	assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 2})

	result, err = subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["five","six"]}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "subagent-5") || !strings.Contains(result.Output, "subagent-6") {
		t.Fatalf("launch after partial wait = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitReturnsMixedResults(t *testing.T) {
	failure := errors.New("child failed")
	subagents := NewSubagent(func(_ context.Context, task string, _ SubagentModelProfile, _ agent.ThinkingLevel, _ func(SubagentProgress)) (agent.RunResult, error) {
		if task == "bad" {
			return agent.RunResult{Text: "partial finding"}, failure
		}
		return agent.RunResult{Text: "useful finding"}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["good","bad"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	result, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1","subagent-2"]}`), nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "useful finding") || !strings.Contains(result.Output, "partial finding") || !strings.Contains(result.Output, failure.Error()) {
		t.Fatalf("wait = %+v, error = %v", result, err)
	}
}

func TestSubagentWaitValidatesIDsBeforeWaiting(t *testing.T) {
	subagents := NewSubagent(func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "done"}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	for _, arguments := range []string{
		`{"ids":[]}`,
		`{"ids":["subagent-1","subagent-1"]}`,
		`{"ids":[" "]}`,
		`{"ids":["missing"]}`,
	} {
		result, err := wait.Execute(context.Background(), json.RawMessage(arguments), nil)
		if err != nil || !result.IsError {
			t.Fatalf("arguments = %s, result = %+v, error = %v", arguments, result, err)
		}
	}
}

func TestSubagentWaitBoundsCombinedOutput(t *testing.T) {
	subagents := NewSubagent(func(context.Context, string, SubagentModelProfile, agent.ThinkingLevel, func(SubagentProgress)) (agent.RunResult, error) {
		return agent.RunResult{Text: strings.Repeat("x", defaultMaxBytes)}, nil
	})
	defer subagents.Close()
	wait := NewSubagentWait(subagents)

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["one","two"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	result, err := wait.Execute(context.Background(), json.RawMessage(`{"ids":["subagent-1","subagent-2"]}`), nil)
	if err != nil || len(result.Output) > defaultMaxBytes || !strings.Contains(result.Output, "subagent output truncated") {
		t.Fatalf("output bytes = %d, error = %v", len(result.Output), err)
	}
}

func TestSubagentPublishesFinalizingStatus(t *testing.T) {
	finalizing := make(chan struct{})
	release := make(chan struct{})
	subagents := NewSubagent(func(_ context.Context, _ string, _ SubagentModelProfile, _ agent.ThinkingLevel, update func(SubagentProgress)) (agent.RunResult, error) {
		usage := agent.Usage{InputTokens: 190_000, OutputTokens: 10_000, TotalTokens: 200_000}
		update(SubagentProgress{
			Usage:              usage,
			Generations:        20,
			Finalizing:         true,
			FinalizationReason: agent.FinalizationReasonGenerations,
		})
		close(finalizing)
		<-release
		return agent.RunResult{Text: "final", Usage: usage}, nil
	})
	defer subagents.Close()

	if result, err := subagents.Execute(context.Background(), json.RawMessage(`{"tasks":["inspect"]}`), nil); err != nil || result.IsError {
		t.Fatalf("launch = %+v, error = %v", result, err)
	}
	<-finalizing
	deadline := time.After(2 * time.Second)
	for {
		select {
		case status := <-subagents.StatusUpdates():
			if status.Finalizing != 1 || len(status.Jobs) != 1 {
				continue
			}
			job := status.Jobs[0]
			if job.State != agent.SubagentFinalizing || job.Usage.InputTokens != 190_000 || job.Usage.OutputTokens != 10_000 || job.Generations != 20 || job.GenerationLimit != 20 || job.FinalizationReason != agent.FinalizationReasonGenerations {
				t.Fatalf("finalizing job = %+v", job)
			}
			close(release)
			assertSubagentStatus(t, subagents.StatusUpdates(), agent.SubagentStatus{Completed: 1})
			return
		case <-deadline:
			t.Fatal("finalizing status was not published")
		}
	}
}

func TestSubagentWaitPresentationIsCompact(t *testing.T) {
	presentation := NewSubagentWait(nil).Presentation(PresentationSnapshot{Arguments: map[string]any{"ids": []any{"subagent-1", "subagent-2"}}})
	if presentation.Title != "subagent_wait" || !slices.Equal(presentation.Lines, []string{"Waiting for 2 subagent(s)."}) {
		t.Fatalf("presentation = %+v", presentation)
	}
}

type recordingSubagentUpdates struct {
	mu      sync.Mutex
	updates []agent.ToolPresentation
	final   agent.ToolPresentation
}

func (updates *recordingSubagentUpdates) Update(presentation agent.ToolPresentation) error {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	updates.updates = append(updates.updates, presentation)
	return nil
}

func (updates *recordingSubagentUpdates) SetFinal(presentation agent.ToolPresentation) {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	updates.final = presentation
}

func assertSubagentStatus(t *testing.T, updates <-chan agent.SubagentStatus, want agent.SubagentStatus) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-updates:
			if got.Running == want.Running && got.Finalizing == want.Finalizing && got.Completed == want.Completed {
				return
			}
		case <-deadline:
			t.Fatalf("subagent status did not reach %+v", want)
		}
	}
}
