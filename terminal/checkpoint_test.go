package terminal

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/keys"
)

func TestTerminalCheckpointRoundTrip(t *testing.T) {
	model := newTUIModel(80, 24, Options{MessageHistory: MessageHistory{Entries: []string{"global prompt"}}})
	model.appendUserContent(textContent("  First line of the prompt  \nsecond line"))
	model.appendEntry(session.EntryAssistant, "answer")
	model.running = false
	model.subagentStatus = subagent.Status{Active: []subagent.JobStatus{{State: subagent.StateRunning}}, PendingCompletions: []subagent.Completion{{Status: subagent.StateComplete}}}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	queued := [][]agent.ContentPart{testTextContent("accepted"), testTextContent("deferred")}

	checkpoint := checkpointModel(model, queued)
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := newTUIModel(100, 30, Options{
		Config:         Config{InitialCheckpoint: &decoded, ContextTokens: 42},
		MessageHistory: MessageHistory{Entries: []string{"global prompt"}},
	})
	if restored.running || restored.activity.kind != activityReady || len(restored.subagentStatus.Active) != 0 {
		t.Fatalf("runtime state was restored: %+v", restored)
	}
	if len(restored.transcript.Entries) != 2 || restored.transcript.Entries[0].Kind != session.EntryUser || restored.transcript.Entries[1].Text != "answer" {
		t.Fatalf("blocks = %+v", restored.transcript.Entries)
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

func TestTerminalCheckpointSnapshotsOpenStream(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	streamAssistantText(model, "first")
	streamAssistantText(model, " second")
	checkpoint := checkpointModel(model, nil)

	streamAssistantText(model, " third")
	if checkpoint.Entries[0].Text != "first second" || model.transcript.Entries[0].Text != "first second third" {
		t.Fatalf("checkpoint=%q model=%q", checkpoint.Entries[0].Text, model.transcript.Entries[0].Text)
	}

	restored := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	streamAssistantText(restored, "new")
	if len(restored.transcript.Entries) != 2 || restored.transcript.Entries[0].Text != "first second" || restored.transcript.Entries[1].Text != "new" {
		t.Fatalf("restored blocks = %+v", restored.transcript.Entries)
	}
}

func TestTerminalCheckpointProjectsDraftImagesOut(t *testing.T) {
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

	checkpoint := checkpointModel(model, nil)
	if checkpoint.Input != "before after" || checkpoint.Cursor != len([]rune("before afte")) {
		t.Fatalf("input = %q, cursor = %d", checkpoint.Input, checkpoint.Cursor)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cG5n") {
		t.Fatalf("draft image was persisted: %s", encoded)
	}
}

func TestTerminalCheckpointProjectsQueuedImagesOut(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	queued := [][]agent.ContentPart{{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: "after"},
	}, {
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("other")}},
	}}

	checkpoint := checkpointModel(model, queued)
	if len(checkpoint.QueuedInputs) != 1 || checkpoint.QueuedInputs[0] != "before after" {
		t.Fatalf("queued inputs = %q", checkpoint.QueuedInputs)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cG5n") || strings.Contains(string(encoded), "b3RoZXI=") {
		t.Fatalf("queued image was persisted: %s", encoded)
	}
}

func TestTerminalCheckpointPreservesInlineImagePositions(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.appendUserContent([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: " after"},
	})
	checkpoint := checkpointModel(model, nil)
	if len(checkpoint.Entries[0].Content) != 3 || checkpoint.Entries[0].Content[1].Kind != agent.ContentPartImage {
		t.Fatalf("checkpoint content = %+v", checkpoint.Entries[0].Content)
	}

	restored := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	content := restored.transcript.Entries[0].Content
	if len(content) != 3 || content[0].Text != "before " || content[1].Kind != agent.ContentPartImage || content[2].Text != " after" {
		t.Fatalf("restored content = %+v", content)
	}
}

func TestTerminalCheckpointRestoreClampsCursor(t *testing.T) {
	checkpoint := Checkpoint{Input: "ab", Cursor: 9}

	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	if model.inputText() != "ab" || model.cursor != 2 {
		t.Fatalf("input = %q, cursor = %d", model.inputText(), model.cursor)
	}
}

func TestPreviousActiveSessionShowsWarning(t *testing.T) {
	checkpoint := Checkpoint{}
	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint, PreviousTurnActive: true}})
	if len(model.transcript.Entries) != 1 || model.transcript.Entries[0].Kind != session.EntryInfo {
		t.Fatalf("blocks = %+v", model.transcript.Entries)
	}
}

func TestStartupWarningsAreShownAfterRestoredConversation(t *testing.T) {
	checkpoint := Checkpoint{}
	warning := "opaque-warning-sentinel"
	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint, Warnings: []string{warning}}})
	if len(model.transcript.Entries) != 1 || model.transcript.Entries[0].Kind != session.EntryInfo || !strings.Contains(model.transcript.Entries[0].Text, warning) {
		t.Fatalf("blocks = %+v", model.transcript.Entries)
	}
}

func TestResumePickerUsesNewestSessionAndReturnsSelection(t *testing.T) {
	const (
		oldID = "0123456789abcdef0123456789abcdef"
		newID = "fedcba9876543210fedcba9876543210"
	)
	now := time.Now()
	model := newTUIModel(80, 24, Options{})
	model.openResumePicker([]SessionSummary{
		{ID: oldID, Description: "old prompt", UpdatedAt: now.Add(-time.Hour)},
		{ID: newID, Description: "new prompt", UpdatedAt: now, Active: true},
	})

	if session, ok := model.resumePicker.current(); !ok || session.ID != newID {
		t.Fatalf("selected=%q exists=%v", session.ID, ok)
	}
	frame := renderFrame(model)
	if !strings.Contains(frame, "new prompt") || !strings.Contains(frame, newID) || !strings.Contains(frame, oldID) {
		t.Fatalf("frame = %q", frame)
	}
	action, handled := reduceResumePickerKey(model, keys.Event{Code: keys.Down})
	if !handled || action.kind != tuiActionNone {
		t.Fatalf("down action=%+v handled=%v", action, handled)
	}
	action, handled = reduceResumePickerKey(model, keys.Event{Code: keys.Enter})
	if !handled || action.kind != tuiActionResume || action.text != oldID {
		t.Fatalf("enter action=%+v handled=%v", action, handled)
	}
}

func TestEngineSnapshotWaitsForTerminalState(t *testing.T) {
	messages := make(chan engineMessage, 2)
	stopped := make(chan struct{})
	defer close(stopped)
	go runEngineOperation(context.Background(), messages, stopped, func(stream EventStream) error {
		_, err := stream.Snapshot()
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
	snapshot.snapshot <- Checkpoint{}
	completed := <-messages
	if !completed.done || completed.err != nil {
		t.Fatalf("completion = %+v", completed)
	}
}

func TestResumeCommandListsSessionsWithoutStoreDependency(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("/resume"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model:      model,
		renderer:   &tuiRenderer{},
		operations: operationsFor(&fakeEngine{}), controls: controlsFor(&fakeEngine{}),
		output:         io.Discard,
		engineMessages: make(chan engineMessage, 1),
		stopped:        make(chan struct{}),
		sessions: Sessions{List: func(context.Context) ([]SessionSummary, error) {
			return []SessionSummary{{ID: "session", Description: "prompt"}}, nil
		}},
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	if !model.resumePicker.active {
		t.Fatal("resume picker did not open")
	}
	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}})
	if err != nil || !exit || controller.outcome != (RunOutcome{Action: RunResumeSession, SessionID: "session"}) {
		t.Fatalf("resume exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
}
