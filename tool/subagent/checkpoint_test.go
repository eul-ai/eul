package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestCheckpointRoundTripRestoresPendingAndInterruptsActive(t *testing.T) {
	block := make(chan struct{})
	manager := NewManager(func(ctx context.Context, task string, _ Profile, _ agent.ThinkingLevel, _ func(Progress)) (agent.RunResult, error) {
		if task == "complete" {
			return agent.RunResult{Text: "finished"}, nil
		}
		select {
		case <-ctx.Done():
			return agent.RunResult{}, ctx.Err()
		case <-block:
			return agent.RunResult{}, nil
		}
	}, nil)
	if _, err := manager.start([]task{{Description: "finished", Prompt: "complete"}, {Description: "running", Prompt: "running"}}, ProfileBalanced, agent.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return len(status.Awaiting) == 1 && len(status.Active) == 1 })
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
	restored := NewManager(func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: "restored"}, nil
	}, nil)
	defer restored.Close()
	if err := restored.RestoreCheckpoint(decoded); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, restored, func(status Status) bool { return len(status.Awaiting) == 2 })
	if status.Awaiting[0].Status != StateComplete || status.Awaiting[1].Status != StateInterrupted {
		t.Fatalf("awaiting = %+v", status.Awaiting)
	}
	if status.Awaiting[1].MessageID <= status.Awaiting[0].MessageID {
		t.Fatalf("message IDs = %d, %d", status.Awaiting[0].MessageID, status.Awaiting[1].MessageID)
	}
	jobs, err := restored.start([]task{{Description: "next", Prompt: "complete"}}, ProfileBalanced, agent.ThinkingLow)
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].id != "subagent-3" {
		t.Fatalf("restored job ID = %q", jobs[0].id)
	}
}
