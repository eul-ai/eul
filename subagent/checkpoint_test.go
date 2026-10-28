package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestCheckpointRoundTripRestoresPendingAndInterruptsActive(t *testing.T) {
	block := make(chan struct{})
	manager := NewManager(func(ctx context.Context, task Task, _ func(agent.Usage)) (string, error) {
		if task.Prompt == "complete" {
			return "finished", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-block:
			return "", nil
		}
	}, Checkpoint{})
	if _, err := manager.Start([]Task{
		{Description: "finished", Prompt: "complete", ModelProfile: ProfileMain, ThinkingLevel: agent.ThinkingMedium},
		{Description: "running", Prompt: "running", ModelProfile: ProfileFast, ThinkingLevel: agent.ThinkingLow},
	}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 1 && len(status.Active) == 1 })
	checkpoint := manager.Checkpoint()
	manager.Close()

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restored := NewManager(func(context.Context, Task, func(agent.Usage)) (string, error) {
		return "restored", nil
	}, decoded)
	defer restored.Close()
	status := restored.Snapshot()
	if len(status.PendingCompletions) != 2 || status.PendingCompletions[0].Status != StateComplete || status.PendingCompletions[1].Status != StateInterrupted {
		t.Fatalf("awaiting = %+v", status.PendingCompletions)
	}
	if status.PendingCompletions[1].MessageID <= status.PendingCompletions[0].MessageID {
		t.Fatalf("message IDs = %d, %d", status.PendingCompletions[0].MessageID, status.PendingCompletions[1].MessageID)
	}
	jobs, err := restored.Start([]Task{{Description: "next", Prompt: "complete"}})
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].ID != "subagent-3" {
		t.Fatalf("restored job ID = %q", jobs[0].ID)
	}
	completion := waitForStatus(t, restored, func(status Status) bool { return len(status.PendingCompletions) == 3 }).PendingCompletions[2]
	if completion.MessageID <= status.PendingCompletions[1].MessageID {
		t.Fatalf("new message ID = %d after %d", completion.MessageID, status.PendingCompletions[1].MessageID)
	}
}
