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
	releases := make(chan struct{}, maxActive+1)
	manager := NewManager(Config{Runner: RunFunc(func(ctx context.Context, request RunRequest, _ func(Progress)) (agent.RunResult, error) {
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-releases:
			return agent.RunResult{Text: "result for " + request.Task}, nil
		}
	})})
	defer manager.Close()

	if _, err := manager.Start(testTasks(maxActive)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(testTasks(1)); err == nil {
		t.Fatal("fifth active subagent was accepted")
	}

	releases <- struct{}{}
	waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 1 })
	if _, err := manager.Start(testTasks(1)); err != nil {
		t.Fatalf("completion did not free capacity: %v", err)
	}
	batch := manager.SnapshotInbox()
	if len(batch.MessageIDs) != 1 || !strings.Contains(batch.Text, "result for task-") {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestStartValidatesLaunchPolicyBeforeMutation(t *testing.T) {
	manager := NewManager(Config{
		Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
			return agent.RunResult{}, nil
		}),
		SupportedThinkingLevels: func(Profile) []agent.ThinkingLevel {
			return []agent.ThinkingLevel{agent.ThinkingMedium}
		},
	})
	defer manager.Close()

	tests := []struct {
		name  string
		tasks []Task
	}{
		{name: "no tasks"},
		{name: "too many tasks", tasks: testTasks(maxActive + 1)},
		{name: "blank description", tasks: []Task{{Prompt: "task"}}},
		{name: "blank prompt", tasks: []Task{{Description: "task"}}},
		{name: "unknown profile", tasks: []Task{{Description: "task", Prompt: "task", ModelProfile: Profile("unknown"), ThinkingLevel: agent.ThinkingMedium}}},
		{name: "invalid thinking", tasks: []Task{{Description: "task", Prompt: "task", ModelProfile: ProfileBalanced, ThinkingLevel: agent.ThinkingLevel("invalid")}}},
		{name: "disallowed thinking", tasks: []Task{{Description: "task", Prompt: "task", ModelProfile: ProfileBalanced, ThinkingLevel: agent.ThinkingMax}}},
		{name: "unsupported thinking", tasks: []Task{{Description: "task", Prompt: "task", ModelProfile: ProfileBalanced, ThinkingLevel: agent.ThinkingLow}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Start(test.tasks); err == nil {
				t.Fatal("invalid launch accepted")
			}
			checkpoint := manager.Checkpoint()
			if checkpoint.data.NextID != 0 || len(checkpoint.data.Active) != 0 {
				t.Fatalf("manager mutated after rejection: %+v", checkpoint.data)
			}
		})
	}
}

func TestStartUsesPerTaskPolicy(t *testing.T) {
	requests := make(chan RunRequest, 2)
	manager := NewManager(Config{Runner: RunFunc(func(_ context.Context, request RunRequest, _ func(Progress)) (agent.RunResult, error) {
		requests <- request
		return agent.RunResult{}, nil
	})})
	defer manager.Close()

	jobs, err := manager.Start([]Task{
		{Description: "fast", Prompt: "fast task", ModelProfile: ProfileFast, ThinkingLevel: agent.ThinkingMinimal},
		{Description: "main", Prompt: "main task", ModelProfile: ProfileMain, ThinkingLevel: agent.ThinkingHigh},
	})
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].ModelProfile != ProfileFast || jobs[0].ThinkingLevel != agent.ThinkingMinimal || jobs[1].ModelProfile != ProfileMain || jobs[1].ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("jobs = %+v", jobs)
	}

	byTask := make(map[string]RunRequest, 2)
	for range 2 {
		request := <-requests
		byTask[request.Task] = request
	}
	if byTask["fast task"].Profile != ProfileFast || byTask["fast task"].ThinkingLevel != agent.ThinkingMinimal {
		t.Fatalf("fast request = %+v", byTask["fast task"])
	}
	if byTask["main task"].Profile != ProfileMain || byTask["main task"].ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("main request = %+v", byTask["main task"])
	}
}

func TestCancelValidatesIDsAtomically(t *testing.T) {
	release := make(chan struct{})
	manager := NewManager(Config{Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
		<-release
		return agent.RunResult{}, nil
	})})
	defer manager.Close()

	jobs, err := manager.Start(testTasks(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{
		nil,
		{""},
		{jobs[0].ID, jobs[0].ID},
		{"missing"},
		{jobs[0].ID, "missing"},
		{"1", "2", "3", "4", "5"},
	} {
		if _, err := manager.Cancel(ids); err == nil {
			t.Fatalf("invalid IDs accepted: %v", ids)
		}
		status := manager.Checkpoint().data.Active
		if len(status) != 2 || status[0].State != StateRunning || status[1].State != StateRunning {
			t.Fatalf("cancel partially mutated jobs for %v: %+v", ids, status)
		}
	}

	close(release)
}

func TestCompleteFailedAndCanceledChildrenQueueTerminalNotifications(t *testing.T) {
	canceled := make(chan struct{})
	manager := NewManager(Config{Runner: RunFunc(func(ctx context.Context, request RunRequest, _ func(Progress)) (agent.RunResult, error) {
		switch request.Task {
		case "complete":
			return agent.RunResult{Text: "done"}, nil
		case "failed":
			return agent.RunResult{}, errors.New("failed")
		default:
			close(canceled)
			<-ctx.Done()
			return agent.RunResult{}, ctx.Err()
		}
	})})
	defer manager.Close()
	jobs, err := manager.Start([]Task{
		{Description: "complete", Prompt: "complete"},
		{Description: "failed", Prompt: "failed"},
		{Description: "cancel", Prompt: "cancel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-canceled
	if _, err := manager.Cancel([]string{jobs[2].ID}); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 3 })
	states := make(map[State]bool, 3)
	for _, completion := range status.PendingCompletions {
		states[completion.Status] = true
	}
	if !states[StateComplete] || !states[StateFailed] || !states[StateCanceled] {
		t.Fatalf("awaiting = %+v", status.PendingCompletions)
	}
}

func TestWaitBlocksUntilCompletionAndDoesNotDrainInbox(t *testing.T) {
	release := make(chan struct{})
	manager := NewManager(Config{Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
		<-release
		return agent.RunResult{Text: "done"}, nil
	})})
	defer manager.Close()
	if _, err := manager.Start(testTasks(1)); err != nil {
		t.Fatal(err)
	}

	type waitResult struct {
		outcome WaitOutcome
		err     error
	}
	waitDone := make(chan waitResult, 1)
	go func() {
		outcome, err := manager.Wait(context.Background())
		waitDone <- waitResult{outcome: outcome, err: err}
	}()
	select {
	case result := <-waitDone:
		t.Fatalf("wait returned early: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if result := <-waitDone; result.err != nil || result.outcome != WaitCompletion {
		t.Fatalf("wait result = %+v", result)
	}
	if len(manager.SnapshotInbox().MessageIDs) != 1 {
		t.Fatal("wait drained inbox")
	}
}

func TestWaitCancellationDoesNotCancelChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := NewManager(Config{Runner: RunFunc(func(ctx context.Context, _ RunRequest, _ func(Progress)) (agent.RunResult, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-release:
			return agent.RunResult{Text: "done"}, nil
		}
	})})
	defer manager.Close()
	if _, err := manager.Start(testTasks(1)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	select {
	case <-childDone:
		t.Fatal("canceled wait canceled child")
	default:
	}

	close(release)
	if outcome, err := manager.Wait(context.Background()); err != nil || outcome != WaitCompletion {
		t.Fatalf("wait outcome = %v, error = %v", outcome, err)
	}
	<-childDone
}

func TestInboxBoundsDescriptionResultAndBatch(t *testing.T) {
	manager := NewManager(Config{Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: strings.Repeat("é\n", maxCompletionResultLines+100) + strings.Repeat("x", maxCompletionResultBytes)}, nil
	})})
	defer manager.Close()
	if _, err := manager.Start([]Task{{Description: strings.Repeat("description", 1_000), Prompt: "task"}}); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 1 })
	completion := status.PendingCompletions[0]
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
	manager := NewManager(Config{Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "done"}, nil
	})})
	defer manager.Close()
	if _, err := manager.Start(testTasks(2)); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 2 })

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

func testTasks(count int) []Task {
	tasks := make([]Task, count)
	for index := range tasks {
		tasks[index] = Task{Description: "task", Prompt: "task-" + string(rune('1'+index))}
	}
	return tasks
}

func waitForStatus(t *testing.T, manager *Manager, ready func(Status) bool) Status {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case status := <-manager.StatusChanges():
			if ready(status) {
				return status
			}
		case <-timer.C:
			t.Fatal("timed out waiting for subagent status")
		}
	}
}
