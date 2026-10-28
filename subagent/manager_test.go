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
	manager := NewManager(func(ctx context.Context, task Task, _ func(agent.Usage)) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-releases:
			return "result for " + task.Prompt, nil
		}
	}, Checkpoint{})
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
	completions := manager.Snapshot().PendingCompletions
	if len(completions) != 1 || !strings.Contains(completions[0].Result, "result for task-") {
		t.Fatalf("completions = %+v", completions)
	}
}

func TestStartValidatesTasksBeforeMutation(t *testing.T) {
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		return "", nil
	}, Checkpoint{})
	defer manager.Close()

	tests := []struct {
		name  string
		tasks []Task
	}{
		{name: "no tasks"},
		{name: "too many tasks", tasks: testTasks(maxActive + 1)},
		{name: "blank description", tasks: []Task{{Prompt: "task"}}},
		{name: "blank prompt", tasks: []Task{{Description: "task"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Start(test.tasks); err == nil {
				t.Fatal("invalid launch accepted")
			}
			checkpoint := manager.Checkpoint()
			if checkpoint.NextID != 0 || len(checkpoint.Completions) != 0 {
				t.Fatalf("manager mutated after rejection: %+v", checkpoint)
			}
		})
	}
}

func TestStartUsesPerTaskPolicy(t *testing.T) {
	requests := make(chan Task, 2)
	manager := NewManager(func(_ context.Context, task Task, _ func(agent.Usage)) (string, error) {
		requests <- task
		return "", nil
	}, Checkpoint{})
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

	byPrompt := make(map[string]Task, 2)
	for range 2 {
		task := <-requests
		byPrompt[task.Prompt] = task
	}
	if byPrompt["fast task"].ModelProfile != ProfileFast || byPrompt["fast task"].ThinkingLevel != agent.ThinkingMinimal {
		t.Fatalf("fast task = %+v", byPrompt["fast task"])
	}
	if byPrompt["main task"].ModelProfile != ProfileMain || byPrompt["main task"].ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("main task = %+v", byPrompt["main task"])
	}
}

func TestCancelValidatesIDsAtomically(t *testing.T) {
	release := make(chan struct{})
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		<-release
		return "", nil
	}, Checkpoint{})
	defer manager.Close()

	jobs, err := manager.Start(testTasks(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{
		nil,
		{""},
		{"missing"},
		{jobs[0].ID, "missing"},
		{"1", "2", "3", "4", "5"},
	} {
		if _, err := manager.Cancel(ids); err == nil {
			t.Fatalf("invalid IDs accepted: %v", ids)
		}
		status := manager.Snapshot().Active
		if len(status) != 2 || status[0].State != StateRunning || status[1].State != StateRunning {
			t.Fatalf("cancel partially mutated jobs for %v: %+v", ids, status)
		}
	}

	close(release)
}

func TestCompleteFailedAndCanceledChildrenQueueTerminalNotifications(t *testing.T) {
	canceled := make(chan struct{})
	manager := NewManager(func(ctx context.Context, task Task, _ func(agent.Usage)) (string, error) {
		switch task.Prompt {
		case "complete":
			return "done", nil
		case "failed":
			return "", errors.New("failed")
		default:
			close(canceled)
			<-ctx.Done()
			return "", ctx.Err()
		}
	}, Checkpoint{})
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
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		<-release
		return "done", nil
	}, Checkpoint{})
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
	if len(manager.Snapshot().PendingCompletions) != 1 {
		t.Fatal("wait drained inbox")
	}
}

func TestWaitCancellationDoesNotCancelChild(t *testing.T) {
	release := make(chan struct{})
	childDone := make(chan struct{})
	manager := NewManager(func(ctx context.Context, _ Task, _ func(agent.Usage)) (string, error) {
		defer close(childDone)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "done", nil
		}
	}, Checkpoint{})
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

func TestInboxBoundsDescriptionAndResult(t *testing.T) {
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		return strings.Repeat("<", maxCompletionBytes) + strings.Repeat("\né", maxCompletionLines+100), nil
	}, Checkpoint{})
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
	lines := strings.Count(completion.Result, "\n") + 1
	if len(completion.Description) > maxTaskDescriptionBytes || len(encoded) > maxCompletionBytes || lines > maxCompletionLines {
		t.Fatalf("description bytes = %d, completion bytes = %d, result lines = %d", len(completion.Description), len(encoded), lines)
	}
}

func TestInboxAcknowledgesOnlyExactPrefix(t *testing.T) {
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		return "done", nil
	}, Checkpoint{})
	defer manager.Close()
	if _, err := manager.Start(testTasks(2)); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 2 })

	completions := manager.Snapshot().PendingCompletions
	messageIDs := make([]uint64, len(completions))
	for index, completion := range completions {
		messageIDs[index] = completion.MessageID
	}
	bad := append([]uint64(nil), messageIDs...)
	bad[0]++
	if err := manager.AcknowledgeCompletions(bad); err == nil {
		t.Fatal("mismatched acknowledgement succeeded")
	}
	if err := manager.AcknowledgeCompletions(messageIDs); err != nil {
		t.Fatal(err)
	}
	if next := manager.Snapshot().PendingCompletions; len(next) != 0 {
		t.Fatalf("pending completions = %+v", next)
	}
}

func TestAcknowledgeCompletionsAfterClose(t *testing.T) {
	manager := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		return "done", nil
	}, Checkpoint{})
	if _, err := manager.Start(testTasks(1)); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 1 })
	manager.Close()

	if err := manager.AcknowledgeCompletions([]uint64{status.PendingCompletions[0].MessageID}); err != nil {
		t.Fatal(err)
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
	timeout := time.After(2 * time.Second)
	for {
		changes := manager.Changes()
		if status := manager.Snapshot(); ready(status) {
			return status
		}
		select {
		case <-changes:
		case <-timeout:
			t.Fatal("timed out waiting for subagent status")
		}
	}
}
