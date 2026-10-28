package terminal

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
)

func TestDraftRoundTrip(t *testing.T) {
	model := newTUIModel(80, 24, Options{MessageHistory: MessageHistory{Entries: []string{"global prompt"}}})
	model.appendUserContent(textContent("prompt"))
	model.appendEntry(session.EntryAssistant, "answer")
	model.subagentStatus = subagent.Status{Active: []subagent.JobStatus{{State: subagent.StateRunning}}, PendingCompletions: []subagent.Completion{{Status: subagent.StateComplete}}}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	model.steering.accepted = [][]agent.ContentPart{testTextContent("accepted")}
	model.steering.deferred = [][]agent.ContentPart{testTextContent("deferred")}

	encoded, err := json.Marshal(model.draft())
	if err != nil {
		t.Fatal(err)
	}
	var decoded session.Draft
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := newTUIModel(100, 30, Options{
		Config:         Config{Transcript: model.transcript.Entries, Draft: decoded, ContextTokens: 42},
		MessageHistory: MessageHistory{Entries: []string{"global prompt"}},
	})
	if restored.running || restored.activity.kind != activityReady || len(restored.subagentStatus.Active) != 0 {
		t.Fatalf("runtime state was restored: %+v", restored)
	}
	if len(restored.transcript.Entries) != 2 || restored.transcript.Entries[0].Kind != session.EntryUser || restored.transcript.Entries[1].Text != "answer" {
		t.Fatalf("entries = %+v", restored.transcript.Entries)
	}
	if restored.inputText() != "accepted\n\ndeferred\n\ndraft" || restored.cursor != len(restored.input) {
		t.Fatalf("input=%q cursor=%d", restored.inputText(), restored.cursor)
	}
	if !slices.Equal(restored.globalHistory, []string{"global prompt"}) || len(restored.history) != 0 {
		t.Fatalf("global history = %q, history = %q", restored.globalHistory, restored.history)
	}
	if restored.contextTokens != 42 {
		t.Fatalf("context tokens = %d", restored.contextTokens)
	}
}

func TestDraftProjectsImagesOut(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("before "); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{MediaType: "image/png", Data: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	if err := model.insertInput("after"); err != nil {
		t.Fatal(err)
	}
	model.moveLeft()
	model.steering.accepted = [][]agent.ContentPart{{
		{Kind: agent.ContentPartText, Text: "queued "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("queued")}},
	}, {
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("other")}},
	}}

	draft := model.draft()
	if draft.Input != "before after" || draft.Cursor != len([]rune("before afte")) || !slices.Equal(draft.QueuedInputs, []string{"queued "}) {
		t.Fatalf("draft = %+v", draft)
	}
	encoded, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cG5n") || strings.Contains(string(encoded), "cXVldWVk") || strings.Contains(string(encoded), "b3RoZXI=") {
		t.Fatalf("draft image was persisted: %s", encoded)
	}
}

func TestDraftRestoreClampsCursor(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{Draft: session.Draft{Input: "ab", Cursor: 9}}})
	if model.inputText() != "ab" || model.cursor != 2 {
		t.Fatalf("input = %q, cursor = %d", model.inputText(), model.cursor)
	}
}

func TestPreviousActiveSessionShowsWarning(t *testing.T) {
	model := newTUIModel(80, 24, Options{Config: Config{PreviousTurnActive: true}})
	if len(model.transcript.Entries) != 1 || model.transcript.Entries[0].Kind != session.EntryInfo {
		t.Fatalf("entries = %+v", model.transcript.Entries)
	}
}

func TestStartupWarningsAreShownAfterRestoredConversation(t *testing.T) {
	warning := "opaque-warning-sentinel"
	model := newTUIModel(80, 24, Options{Config: Config{Transcript: []session.Entry{{Kind: session.EntryUser, Text: "prompt"}}, Warnings: []string{warning}}})
	if len(model.transcript.Entries) != 2 || model.transcript.Entries[1].Kind != session.EntryInfo || !strings.Contains(model.transcript.Entries[1].Text, warning) {
		t.Fatalf("entries = %+v", model.transcript.Entries)
	}
}

func TestEngineSnapshotWaitsForTerminalState(t *testing.T) {
	messages := make(chan engineMessage, 2)
	stopped := make(chan struct{})
	defer close(stopped)
	go runEngineOperation(context.Background(), messages, stopped, func(stream EventStream) error {
		_, _, err := stream.Snapshot()
		return err
	})

	snapshot := <-messages
	if snapshot.snapshot == nil || snapshot.done {
		t.Fatalf("snapshot message = %+v", snapshot)
	}
	select {
	case message := <-messages:
		t.Fatalf("operation completed before snapshot: %+v", message)
	default:
	}
	snapshot.snapshot <- conversationSnapshot{}
	completed := <-messages
	if !completed.done || completed.err != nil {
		t.Fatalf("completion = %+v", completed)
	}
}
