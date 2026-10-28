package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestCheckpointRejectsInvalidActivePolicy(t *testing.T) {
	base := checkpointData{
		Version: checkpointVersion,
		NextID:  1,
		Active: []activeCheckpoint{{
			ID:            "subagent-1",
			Description:   "task",
			ModelProfile:  ProfileBalanced,
			ThinkingLevel: agent.ThinkingLow,
			State:         StateRunning,
		}},
	}

	invalidProfile := cloneCheckpointData(base)
	invalidProfile.Active[0].ModelProfile = Profile("unknown")
	if err := validateCheckpointData(invalidProfile); err == nil {
		t.Fatal("invalid profile accepted")
	}

	invalidThinking := cloneCheckpointData(base)
	invalidThinking.Active[0].ThinkingLevel = agent.ThinkingLevel("invalid")
	if err := validateCheckpointData(invalidThinking); err == nil {
		t.Fatal("invalid thinking level accepted")
	}
}

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
	})
	if _, err := manager.Start([]Task{
		{Description: "finished", Prompt: "complete", ModelProfile: ProfileMain, ThinkingLevel: agent.ThinkingMedium},
		{Description: "running", Prompt: "running", ModelProfile: ProfileFast, ThinkingLevel: agent.ThinkingLow},
	}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.PendingCompletions) == 1 && len(status.Active) == 1 })
	checkpoint := manager.Checkpoint()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

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
	})
	defer restored.Close()
	if err := restored.RestoreCheckpoint(decoded); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, restored, func(status Status) bool { return len(status.PendingCompletions) == 2 })
	if status.PendingCompletions[0].Status != StateComplete || status.PendingCompletions[1].Status != StateInterrupted {
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
}
