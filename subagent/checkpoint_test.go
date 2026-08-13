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
			Order:         1,
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
	invalidThinking.Active[0].ThinkingLevel = agent.ThinkingMax
	if err := validateCheckpointData(invalidThinking); err == nil {
		t.Fatal("invalid thinking level accepted")
	}
}

func TestCheckpointRoundTripRestoresPendingAndInterruptsActive(t *testing.T) {
	block := make(chan struct{})
	manager := NewManager(Config{Runner: RunFunc(func(ctx context.Context, request RunRequest, _ func(Progress)) (agent.RunResult, error) {
		if request.Task == "complete" {
			return agent.RunResult{Text: "finished"}, nil
		}
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-block:
			return agent.RunResult{}, nil
		}
	})})
	if _, err := manager.Start([]Task{{Description: "finished", Prompt: "complete"}, {Description: "running", Prompt: "running"}}, ProfileBalanced, agent.ThinkingLow); err != nil {
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
	restored := NewManager(Config{Runner: RunFunc(func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "restored"}, nil
	})})
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
	jobs, err := restored.Start([]Task{{Description: "next", Prompt: "complete"}}, ProfileBalanced, agent.ThinkingLow)
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].ID != "subagent-3" {
		t.Fatalf("restored job ID = %q", jobs[0].ID)
	}
}
