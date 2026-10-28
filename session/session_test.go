package session

import (
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func openTestSession(t *testing.T, handle *Handle, warnings ...string) *Session {
	t.Helper()
	engine := agent.New(nil, nil, agent.Options{Checkpoint: handle.Record().Agent})
	subagents := subagent.NewManager(nil, handle.Record().Subagent)
	t.Cleanup(subagents.Close)
	return New(handle, engine, subagents, agent.NewSettings(agent.ThinkingMedium, false), warnings)
}

func countNotices(entries []Entry) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == EntryNotice {
			count++
		}
	}
	return count
}

func TestResumingDoesNotAccumulateNotices(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := t.TempDir()
	handle, err := store.Create(Settings{WorkingDirectory: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.save(state{Transcript: []Entry{{Kind: EntryUser, Text: "prompt"}}}, true, agent.ThinkingMedium, false); err != nil {
		t.Fatal(err)
	}
	id := handle.Record().ID
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	for _, wantNotices := range []int{2, 1, 1} {
		handle, err := store.Open(cwd, id)
		if err != nil {
			t.Fatal(err)
		}
		conversation := openTestSession(t, handle, "config warning")
		entries, _ := conversation.Sync(nil)
		if len(entries) != 1+wantNotices || entries[0].Kind != EntryUser || countNotices(entries) != wantNotices {
			t.Fatalf("entries = %+v, want %d notices", entries, wantNotices)
		}
		if err := conversation.Close(); err != nil {
			t.Fatal(err)
		}
	}

	handle, err = store.Open(cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if transcript := handle.Record().Transcript; len(transcript) != 1 || countNotices(transcript) != 0 {
		t.Fatalf("persisted transcript = %+v", transcript)
	}
}

func TestSessionSavesTurnCheckpointsWithLastDraft(t *testing.T) {
	store := NewStore(t.TempDir())
	handle, err := store.Create(Settings{WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	conversation := openTestSession(t, handle)
	defer conversation.Close()

	conversation.AppendUser([]agent.ContentPart{{Kind: agent.ContentPartText, Text: "prompt"}})
	conversation.SetDraft(Draft{Input: "draft", QueuedInputs: []string{"queued"}})
	var forwarded []agent.EventKind
	sink := conversation.Sink(func(event agent.Event) error {
		forwarded = append(forwarded, event.Kind)
		return nil
	})
	if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if err := sink(agent.Event{Kind: agent.EventCheckpoint, Checkpoint: &agent.Checkpoint{State: []byte("state")}}); err != nil {
		t.Fatal(err)
	}

	record := handle.Record()
	if !slices.Equal(forwarded, []agent.EventKind{agent.EventAssistantText}) {
		t.Fatalf("forwarded events = %v", forwarded)
	}
	if !record.Active || string(record.Agent.State) != "state" || record.Draft.Input != "draft" || !slices.Equal(record.Draft.QueuedInputs, []string{"queued"}) {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Transcript) != 2 || record.Transcript[1].Text != "answer" || record.Description != "prompt" {
		t.Fatalf("transcript = %+v, description = %q", record.Transcript, record.Description)
	}
}
