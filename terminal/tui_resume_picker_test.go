package terminal

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal/keys"
)

func TestResumePickerUsesNewestSessionAndReturnsSelection(t *testing.T) {
	const (
		oldID = "0123456789abcdef0123456789abcdef"
		newID = "fedcba9876543210fedcba9876543210"
	)
	now := time.Now()
	model := newTUIModel(80, 24, Options{})
	model.openResumePicker([]session.Summary{
		{ID: oldID, Description: "old prompt", UpdatedAt: now.Add(-time.Hour)},
		{ID: newID, Description: "new prompt", UpdatedAt: now, Active: true},
	})

	if selected, ok := model.resumePicker.current(); !ok || selected.ID != newID {
		t.Fatalf("selected=%q exists=%v", selected.ID, ok)
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
		sessions: Sessions{List: func(context.Context) ([]session.Summary, error) {
			return []session.Summary{{ID: "session", Description: "prompt"}}, nil
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
