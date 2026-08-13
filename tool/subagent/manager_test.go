package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestTerminalTransitionQueuesCompletionAndFreesCapacity(t *testing.T) {
	releases := make(chan struct{}, MaxActive+1)
	manager := NewManager(func(ctx context.Context, task string, _ Profile, _ agent.ThinkingLevel, _ func(Progress)) (agent.RunResult, error) {
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-releases:
			return agent.RunResult{Text: "result for " + task}, nil
		}
	}, nil)
	defer manager.Close()

	if _, err := manager.start(testTasks(MaxActive), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.start(testTasks(1), ProfileBalanced, agent.ThinkingLow); err == nil {
		t.Fatal("fifth active subagent was accepted")
	}

	releases <- struct{}{}
	waitForStatus(t, manager, func(status Status) bool { return len(status.Awaiting) == 1 })
	if _, err := manager.start(testTasks(1), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatalf("completion did not free capacity: %v", err)
	}
	batch := manager.SnapshotInbox()
	if len(batch.MessageIDs) != 1 || !strings.Contains(batch.Text, "result for task-") {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestCompleteFailedAndCanceledChildrenQueueTerminalNotifications(t *testing.T) {
	canceled := make(chan struct{})
	manager := NewManager(func(ctx context.Context, task string, _ Profile, _ agent.ThinkingLevel, _ func(Progress)) (agent.RunResult, error) {
		switch task {
		case "complete":
			return agent.RunResult{Text: "done"}, nil
		case "failed":
			return agent.RunResult{}, errors.New("failed")
		default:
			close(canceled)
			<-ctx.Done()
			return agent.RunResult{}, ctx.Err()
		}
	}, nil)
	defer manager.Close()
	jobs, err := manager.start([]task{
		{Description: "complete", Prompt: "complete"},
		{Description: "failed", Prompt: "failed"},
		{Description: "cancel", Prompt: "cancel"},
	}, ProfileBalanced, agent.ThinkingLow)
	if err != nil {
		t.Fatal(err)
	}
	<-canceled
	if _, err := manager.cancelIDs([]string{jobs[2].id}); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return len(status.Awaiting) == 3 })
	states := make(map[State]bool, 3)
	for _, completion := range status.Awaiting {
		states[completion.Status] = true
	}
	if !states[StateComplete] || !states[StateFailed] || !states[StateCanceled] {
		t.Fatalf("awaiting = %+v", status.Awaiting)
	}
}

func TestWaitBlocksUntilCompletionAndDoesNotDrainInbox(t *testing.T) {
	release := make(chan struct{})
	manager := NewManager(func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error) {
		<-release
		return agent.RunResult{Text: "done"}, nil
	}, nil)
	defer manager.Close()
	if _, err := manager.start(testTasks(1), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}

	type waitResult struct {
		outcome waitOutcome
		err     error
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		outcome, err := manager.waitForCompletion(context.Background())
		waitDone <- waitResult{outcome: outcome, err: err}
	}()
	select {
	case result := <-waitDone:
		t.Fatalf("wait returned early: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if result := <-waitDone; result.err != nil || result.outcome != waitCompletion {
		t.Fatalf("wait result = %+v", result)
	}
	if len(manager.SnapshotInbox().MessageIDs) != 1 {
		t.Fatal("wait drained inbox")
	}
}

func TestWaitCancellationDoesNotCancelChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := NewManager(func(ctx context.Context, _ string, _ Profile, _ agent.ThinkingLevel, _ func(Progress)) (agent.RunResult, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-release:
			return agent.RunResult{Text: "done"}, nil
		}
	}, nil)
	defer manager.Close()
	if _, err := manager.start(testTasks(1), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.waitForCompletion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	select {
	case <-childDone:
		t.Fatal("canceled wait canceled child")
	default:
	}

	close(release)
	if outcome, err := manager.waitForCompletion(context.Background()); err != nil || outcome != waitCompletion {
		t.Fatalf("wait outcome = %v, error = %v", outcome, err)
	}
	<-childDone
}

func TestWaitToolIsSynchronizationOnly(t *testing.T) {
	manager := NewManager(func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "research result"}, nil
	}, nil)
	defer manager.Close()
	launch := NewLaunchTool(manager)
	wait := NewWaitTool(manager)

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

func TestWaitToolSteeringResultPreservesOriginalTask(t *testing.T) {
	message := waitResultMessage(waitSteering)
	if !strings.Contains(message, "continue the original task") || !strings.Contains(message, "call subagent_wait again") {
		t.Fatalf("steering result = %q", message)
	}
}

func TestWaitToolTimesOutWithoutCancelingChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := NewManager(func(ctx context.Context, _ string, _ Profile, _ agent.ThinkingLevel, _ func(Progress)) (agent.RunResult, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-release:
			return agent.RunResult{Text: "done"}, nil
		}
	}, nil)
	defer manager.Close()
	if _, err := manager.start(testTasks(1), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	wait := NewWaitTool(manager)

	result, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_ms":1}`), nil)
	if err != nil || result.IsError || !strings.Contains(result.Output, "No subagent completion") {
		t.Fatalf("wait result = %+v, error = %v", result, err)
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

func TestWaitToolValidatesTimeout(t *testing.T) {
	manager := NewManager(nil, nil)
	defer manager.Close()
	wait := NewWaitTool(manager)

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

func TestInboxBoundsDescriptionResultAndBatch(t *testing.T) {
	manager := NewManager(func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: strings.Repeat("é\n", maxCompletionResultLines+100) + strings.Repeat("x", maxCompletionResultBytes)}, nil
	}, nil)
	defer manager.Close()
	if _, err := manager.start([]task{{Description: strings.Repeat("description", 1_000), Prompt: "task"}}, ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return len(status.Awaiting) == 1 })
	completion := status.Awaiting[0]
	encoded, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	batch := manager.SnapshotInbox()
	if len(completion.Task) > maxTaskDescriptionBytes || len(encoded) > maxCompletionMessageBytes || len(batch.Text) > maxInboxBatchBytes {
		t.Fatalf("task bytes = %d, completion bytes = %d, batch bytes = %d", len(completion.Task), len(encoded), len(batch.Text))
	}
}

func TestInboxAcknowledgesOnlyExactPrefix(t *testing.T) {
	manager := NewManager(func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "done"}, nil
	}, nil)
	defer manager.Close()
	if _, err := manager.start(testTasks(2), ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.Awaiting) == 2 })

	batch := manager.SnapshotInbox()
	bad := batch
	bad.MessageIDs = append([]uint64(nil), batch.MessageIDs...)
	bad.MessageIDs[0]++
	if err := manager.AcknowledgeInbox(bad); err == nil {
		t.Fatal("mismatched acknowledgement succeeded")
	}
	if err := manager.AcknowledgeInbox(batch); err != nil {
		t.Fatal(err)
	}
	if next := manager.SnapshotInbox(); len(next.MessageIDs) != 0 {
		t.Fatalf("pending batch = %+v", next)
	}
}

func testTasks(count int) []task {
	tasks := make([]task, count)
	for index := range tasks {
		tasks[index] = task{Description: "task", Prompt: "task-" + string(rune('1'+index))}
	}
	return tasks
}

func waitForStatus(t *testing.T, manager *Manager, ready func(Status) bool) Status {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case status := <-manager.StatusUpdates():
			if ready(status) {
				return status
			}
		case <-timer.C:
			t.Fatal("timed out waiting for subagent status")
		}
	}
}
