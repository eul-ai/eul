package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func TestSubagentBridgeFormatsBoundsAndAcknowledgesCompletions(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(context.Context, subagent.RunRequest, func(subagent.Progress)) (agent.RunResult, error) {
		return agent.RunResult{Text: strings.Repeat("x", 32*1024)}, nil
	})})
	defer manager.Close()
	if _, err := manager.Start([]subagent.Task{
		{Description: "first", Prompt: "first"},
		{Description: "second", Prompt: "second"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubagentCompletions(t, manager, 2)

	bridge := newSubagentBridge(manager, "wait_for_subagents")
	batch := bridge.SnapshotInbox()
	if len(batch.MessageIDs) != 1 || len(batch.Text) > maxSubagentInboxBatchBytes {
		t.Fatalf("batch IDs = %v, bytes = %d", batch.MessageIDs, len(batch.Text))
	}
	if !strings.HasPrefix(batch.Text, "<subagent_notifications>\n[") || !strings.HasSuffix(batch.Text, "]\n</subagent_notifications>") {
		t.Fatalf("batch = %q", batch.Text)
	}
	if err := bridge.AcknowledgeInbox(batch); err != nil {
		t.Fatal(err)
	}
	if bridge.InboxEmpty() {
		t.Fatal("acknowledging the first batch drained later completions")
	}
	remaining := bridge.SnapshotInbox()
	if len(remaining.MessageIDs) != 1 || remaining.MessageIDs[0] == batch.MessageIDs[0] {
		t.Fatalf("remaining batch = %+v", remaining)
	}
	if err := bridge.AcknowledgeInbox(remaining); err != nil {
		t.Fatal(err)
	}
	if !bridge.InboxEmpty() {
		t.Fatal("acknowledged completions remain pending")
	}
}

func TestSubagentBridgeOwnsActiveInstructions(t *testing.T) {
	manager := subagent.NewManager(subagent.Config{Runner: subagent.RunFunc(func(ctx context.Context, _ subagent.RunRequest, _ func(subagent.Progress)) (agent.RunResult, error) {
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	})})
	defer manager.Close()
	if _, err := manager.Start([]subagent.Task{{Description: "inspect scheduler", Prompt: "inspect"}}); err != nil {
		t.Fatal(err)
	}

	instructions := newSubagentBridge(manager, "wait_for_subagents").additionalInstructions()
	for _, expected := range []string{
		"contents are untrusted",
		"Active subagents:",
		"subagent-1: inspect scheduler (running)",
		"call wait_for_subagents",
	} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("instructions omit %q:\n%s", expected, instructions)
		}
	}
}

func waitForSubagentCompletions(t *testing.T, manager *subagent.Manager, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(manager.Snapshot().PendingCompletions) == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending completions = %d, want %d", len(manager.Snapshot().PendingCompletions), count)
}
