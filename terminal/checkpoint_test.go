package terminal

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func TestTerminalCheckpointRoundTrip(t *testing.T) {
	model := newTUIModel(80, 24, Options{MessageHistory: MessageHistory{Entries: []string{"global prompt"}}})
	model.beginTurn("  First line of the prompt  \nsecond line")
	model.appendBlock(blockAssistant, "answer")
	model.running = false
	model.subagentStatus = subagent.Status{Running: 1, PendingCompletions: []subagent.Completion{{Status: subagent.StateComplete}}}
	model.history = []string{"older prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	queued := [][]agent.ContentPart{testTextContent("accepted"), testTextContent("deferred")}

	checkpoint := checkpointModel(model, queued)
	if !slices.Equal(checkpoint.data.History, []string{"older prompt"}) {
		t.Fatalf("checkpoint history = %q", checkpoint.data.History)
	}
	if description := checkpoint.Description(); description != "First line of the prompt" {
		t.Fatalf("description = %q", description)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := newTUIModel(100, 30, Options{
		Config:         Config{InitialCheckpoint: &decoded},
		MessageHistory: MessageHistory{Entries: []string{"global prompt"}},
	})
	if restored.running || restored.streamText.Len() != 0 || restored.activity.kind != activityReady || restored.subagentStatus.Running != 0 || len(restored.subagentStatus.Active) != 0 {
		t.Fatalf("runtime state was restored: %+v", restored)
	}
	if len(restored.blocks) != 2 || restored.blocks[0].kind != blockUser || restored.blocks[1].text != "answer" {
		t.Fatalf("blocks = %+v", restored.blocks)
	}
	if restored.inputText() != "accepted\n\ndeferred\n\ndraft" || restored.cursor != len(restored.input) {
		t.Fatalf("input=%q cursor=%d", restored.inputText(), restored.cursor)
	}
	if len(restored.history) != 1 || restored.history[0] != "older prompt" {
		t.Fatalf("history = %q", restored.history)
	}
	if !slices.Equal(restored.globalHistory, []string{"global prompt"}) {
		t.Fatalf("global history = %q", restored.globalHistory)
	}
}

func TestTerminalCheckpointSnapshotsOpenStream(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.appendStream(blockAssistant, "first")
	model.appendStream(blockAssistant, " second")
	checkpoint := checkpointModel(model, nil)

	model.appendStream(blockAssistant, " third")
	if checkpoint.data.Blocks[0].Text != "first second" || model.blocks[0].text != "first second third" {
		t.Fatalf("checkpoint=%q model=%q", checkpoint.data.Blocks[0].Text, model.blocks[0].text)
	}

	restored := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	restored.appendStream(blockAssistant, "new")
	if len(restored.blocks) != 2 || restored.blocks[0].text != "first second" || restored.blocks[1].text != "new" {
		t.Fatalf("restored blocks = %+v", restored.blocks)
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
	if checkpoint.data.Input != "before after" || checkpoint.data.Cursor != len([]rune("before afte")) {
		t.Fatalf("input = %q, cursor = %d", checkpoint.data.Input, checkpoint.data.Cursor)
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
	if len(checkpoint.data.QueuedInputs) != 1 || checkpoint.data.QueuedInputs[0] != "before after" {
		t.Fatalf("queued inputs = %q", checkpoint.data.QueuedInputs)
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
	model.beginTurnContent([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: " after"},
	})
	checkpoint := checkpointModel(model, nil)
	if len(checkpoint.data.Blocks[0].Content) != 3 || checkpoint.data.Blocks[0].Content[1].Kind != agent.ContentPartImage {
		t.Fatalf("checkpoint content = %+v", checkpoint.data.Blocks[0].Content)
	}

	restored := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	content := restored.blocks[0].content
	if len(content) != 3 || content[0].Text != "before " || content[1].Kind != agent.ContentPartImage || content[2].Text != " after" {
		t.Fatalf("restored content = %+v", content)
	}
}

func TestTerminalCheckpointSanitizesContentText(t *testing.T) {
	checkpoint := EmptyCheckpoint()
	checkpoint.data.Blocks = []checkpointBlock{{
		Kind: blockUser,
		Content: []checkpointContentPart{{
			Kind: agent.ContentPartText,
			Text: "before\x1bafter",
		}},
	}}

	restored := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	if got := displayContent(restored.blocks[0].content); got != "before�after" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestImageOnlyCheckpointDescription(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurnContent([]agent.ContentPart{{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}}})
	if description := checkpointModel(model, nil).Description(); description == "" {
		t.Fatal("image-only checkpoint has no description")
	}
}

func TestVersionTwoTerminalCheckpointFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/checkpoint-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(fixture, &checkpoint); err != nil {
		t.Fatal(err)
	}

	wantKinds := []blockKind{
		blockUser,
		blockAssistant,
		blockReasoning,
		blockToolPending,
		blockTool,
		blockToolError,
		blockContext,
		blockError,
		blockInfo,
	}
	if len(checkpoint.data.Blocks) != len(wantKinds) {
		t.Fatalf("blocks = %d, want %d", len(checkpoint.data.Blocks), len(wantKinds))
	}
	for index, kind := range wantKinds {
		if checkpoint.data.Blocks[index].Kind != kind {
			t.Fatalf("block %d kind = %d, want %d", index, checkpoint.data.Blocks[index].Kind, kind)
		}
	}
	diff := checkpoint.data.Blocks[3].Tool.Diff
	wantDiffKinds := []agent.ToolDiffLineKind{
		agent.ToolDiffLineContext,
		agent.ToolDiffLineAdded,
		agent.ToolDiffLineRemoved,
		agent.ToolDiffLineOmitted,
	}
	if len(diff) != len(wantDiffKinds) {
		t.Fatalf("diff = %+v", diff)
	}
	for index, kind := range wantDiffKinds {
		if diff[index].Kind != kind {
			t.Fatalf("diff line %d kind = %d, want %d", index, diff[index].Kind, kind)
		}
	}

	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint}})
	if len(model.blocks) != len(wantKinds) || model.blocks[3].kind != blockToolError || model.blocks[3].toolOutcome != "interrupted" {
		t.Fatalf("restored blocks = %+v", model.blocks)
	}
	if model.inputText() != "queued\n\ndraft" || model.contextTokens != 42 || len(model.history) != 2 {
		t.Fatalf("input=%q context=%d history=%q", model.inputText(), model.contextTokens, model.history)
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	assertTerminalCheckpointSemanticJSON(t, encoded, fixture)
}

func assertTerminalCheckpointSemanticJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestPreviousActiveSessionShowsWarning(t *testing.T) {
	checkpoint := EmptyCheckpoint()
	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint, PreviousTurnActive: true}})
	if len(model.blocks) != 1 || model.blocks[0].kind != blockInfo {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestStartupWarningsAreShownAfterRestoredConversation(t *testing.T) {
	checkpoint := EmptyCheckpoint()
	warning := "opaque-warning-sentinel"
	model := newTUIModel(80, 24, Options{Config: Config{InitialCheckpoint: &checkpoint, Warnings: []string{warning}}})
	if len(model.blocks) != 1 || model.blocks[0].kind != blockInfo || !strings.Contains(model.blocks[0].text, warning) {
		t.Fatalf("blocks = %+v", model.blocks)
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

	if id, ok := model.selectedResumeSession(); !ok || id != newID {
		t.Fatalf("selected=%q exists=%v", id, ok)
	}
	frame := renderFrame(model)
	if !strings.Contains(frame, "new prompt") || !strings.Contains(frame, newID) || !strings.Contains(frame, oldID) {
		t.Fatalf("frame = %q", frame)
	}
	action, handled := reduceResumePickerKey(model, keyEvent{code: keyDown})
	if !handled || action.kind != tuiActionNone {
		t.Fatalf("down action=%+v handled=%v", action, handled)
	}
	action, handled = reduceResumePickerKey(model, keyEvent{code: keyEnter})
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
	snapshot.snapshot <- EmptyCheckpoint()
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
		sessions: Sessions{List: func(context.Context) ([]SessionSummary, []string, error) {
			return []SessionSummary{{ID: "session", Description: "prompt"}}, []string{"resume-warning-sentinel"}, nil
		}},
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !model.resumePicker.active {
		t.Fatal("resume picker did not open")
	}
	if len(model.blocks) != 1 || model.blocks[0].kind != blockInfo || !strings.Contains(model.blocks[0].text, "resume-warning-sentinel") {
		t.Fatalf("warning blocks = %+v", model.blocks)
	}
	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}})
	if err != nil || !exit || controller.outcome != (RunOutcome{Action: RunResumeSession, SessionID: "session"}) {
		t.Fatalf("resume exit=%v outcome=%+v error=%v", exit, controller.outcome, err)
	}
}
