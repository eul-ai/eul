package terminal

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"yaah/agent"
)

func TestTUIControllerDoesNotClearConversationWhenResetFails(t *testing.T) {
	resetErr := errors.New("engine busy")
	engine := &fakeEngine{resetErr: resetErr}
	model := newTUIModel(80, 24, Options{})
	model.appendBlock(blockAssistant, "keep me")
	if err := model.insertInput("/clear"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}})
	if err != nil || exit {
		t.Fatalf("transition exit=%v error=%v", exit, err)
	}
	if len(model.blocks) != 1 || model.blocks[0].text != "keep me" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
	if model.activity.kind != activityError || model.activity.detail != resetErr.Error() {
		t.Fatalf("activity = %+v", model.activity)
	}
}

func TestRenderFailureDoesNotCommitUnseenFrame(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	renderer := &tuiRenderer{}
	dirty := true
	if err := renderIfDirty(renderer, model, failingWriter{}, &dirty, false); !errors.Is(err, errOutput) {
		t.Fatalf("render error = %v", err)
	}
	if renderer.frame.width != 0 || !dirty {
		t.Fatalf("committed frame=%+v dirty=%v", renderer.frame, dirty)
	}
}

func TestMouseSelectionUsesCommittedFrame(t *testing.T) {
	model := newTUIModel(20, 10, Options{})
	model.appendBlock(blockAssistant, "alpha")
	renderer := &tuiRenderer{}
	_ = renderer.render(model)
	committed := renderer.frame

	model.clearConversation()
	model.appendBlock(blockAssistant, "new unseen text")
	reduceMouse(model, mouseEvent{kind: mousePress, column: 1, row: 1}, committed)
	action := reduceMouse(model, mouseEvent{kind: mouseRelease, column: 5, row: 1}, committed)
	if action.kind != tuiActionCopy || action.text != "alpha" || strings.Contains(action.text, "unseen") {
		t.Fatalf("action = %+v", action)
	}
}

func TestMouseWheelUsesCommittedConversationBounds(t *testing.T) {
	model := newTUIModel(20, 8, Options{})
	for range 8 {
		model.appendBlock(blockInfo, "visible line")
	}
	renderer := &tuiRenderer{}
	_ = renderer.render(model)
	committedTop := renderer.frame.conversationTop
	if committedTop < mouseWheelScrollLines {
		t.Fatalf("committed top = %d", committedTop)
	}
	for range 8 {
		model.appendBlock(blockInfo, "unseen line")
	}

	reduceMouse(model, mouseEvent{kind: mouseWheelUp}, renderer.frame)
	if model.scrollTop != committedTop-mouseWheelScrollLines {
		t.Fatalf("scroll top = %d, want %d", model.scrollTop, committedTop-mouseWheelScrollLines)
	}
}

func TestTUIControllerAppliesThinkingLevelOutsideModel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTUIModel(80, 24, Options{SetThinkingLevel: func(agent.ThinkingLevel) error { return nil }})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		setThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		},
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyShiftTab}}); err != nil {
		t.Fatal(err)
	}
	if configured != agent.ThinkingHigh || model.thinkingLevel != agent.ThinkingHigh {
		t.Fatalf("configured=%q model=%q", configured, model.thinkingLevel)
	}
}
