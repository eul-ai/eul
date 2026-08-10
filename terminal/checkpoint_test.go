package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestTerminalCheckpointRoundTrip(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.beginTurn("  First line of the prompt  \nsecond line")
	model.appendBlock(blockAssistant, "answer")
	model.running = false
	model.subagentStatus = agent.SubagentStatus{Running: 1, Completed: 1}
	model.history = []string{"older prompt"}
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	model.queueSteering("accepted")
	model.queueSteering("deferred")

	checkpoint := checkpointModel(model)
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

	restored := newTUIModel(100, 30, Options{InitialCheckpoint: &decoded})
	if restored.running || restored.streamOpen || restored.activity.kind != activityReady || restored.subagentStatus != (agent.SubagentStatus{}) {
		t.Fatalf("runtime state was restored: %+v", restored)
	}
	if len(restored.blocks) != 2 || restored.blocks[0].kind != blockUser || restored.blocks[1].text != "answer" {
		t.Fatalf("blocks = %+v", restored.blocks)
	}
	if string(restored.input) != "accepted\n\ndeferred\n\ndraft" || restored.cursor != len(restored.input) {
		t.Fatalf("input=%q cursor=%d", restored.input, restored.cursor)
	}
	if len(restored.history) != 1 || restored.history[0] != "older prompt" {
		t.Fatalf("history = %q", restored.history)
	}
}

func TestPreviousActiveSessionShowsWarning(t *testing.T) {
	checkpoint := EmptyCheckpoint()
	model := newTUIModel(80, 24, Options{InitialCheckpoint: &checkpoint, PreviousTurnActive: true})
	if len(model.blocks) != 1 || model.blocks[0].kind != blockInfo || !strings.Contains(model.blocks[0].text, "tool side effects may remain") {
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
	if !strings.Contains(frame, "new prompt") || !strings.Contains(frame, newID) || !strings.Contains(frame, oldID) || !strings.Contains(frame, "interrupted") {
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

func TestEngineCheckpointEventWaitsForAcknowledgement(t *testing.T) {
	messages := make(chan engineMessage, 2)
	stopped := make(chan struct{})
	defer close(stopped)
	go runEngineOperation(context.Background(), messages, stopped, func(sink agent.EventSink) error {
		return sink(agent.Event{Kind: agent.EventCheckpoint})
	})

	checkpoint := <-messages
	if checkpoint.ack == nil || checkpoint.done {
		t.Fatalf("checkpoint message = %+v", checkpoint)
	}
	select {
	case message := <-messages:
		t.Fatalf("operation completed before acknowledgement: %+v", message)
	default:
	}
	checkpoint.ack <- nil
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
		model:          model,
		renderer:       &tuiRenderer{},
		engine:         &fakeEngine{},
		output:         io.Discard,
		engineMessages: make(chan engineMessage, 1),
		stopped:        make(chan struct{}),
		listSessions: func(context.Context) ([]SessionSummary, error) {
			return []SessionSummary{{ID: "session", Description: "prompt"}}, nil
		},
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !model.resumePicker.active {
		t.Fatal("resume picker did not open")
	}
	_, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}})
	var request *ResumeRequest
	if !errors.As(err, &request) || request.SessionID != "session" {
		t.Fatalf("resume error = %v", err)
	}
}
