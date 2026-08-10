package terminal

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestTUIControllerEOFWhileRunningDefersCheckpoint(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	canceled := false
	saveCalls := 0
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		turnCancel: func() { canceled = true },
		saveCheckpoint: func(agent.Checkpoint, Checkpoint, bool) error {
			saveCalls++
			return nil
		},
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEOF}})
	if err != nil || exit {
		t.Fatalf("transition exit=%v error=%v", exit, err)
	}
	if !canceled || !errors.Is(controller.exitAfterTurn, io.EOF) || saveCalls != 0 {
		t.Fatalf("canceled=%v exitAfterTurn=%v saveCalls=%d", canceled, controller.exitAfterTurn, saveCalls)
	}
}

func TestTUIControllerNewSessionKeepsCurrentConversation(t *testing.T) {
	engine := &fakeEngine{}
	model := newTUIModel(80, 24, Options{})
	model.appendBlock(blockAssistant, "keep me")
	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}})
	var request *NewSessionRequest
	if !errors.As(err, &request) || exit {
		t.Fatalf("transition exit=%v error=%v", exit, err)
	}
	if len(model.blocks) != 1 || model.blocks[0].text != "keep me" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestTUIControllerCompactsConversation(t *testing.T) {
	engine := &fakeEngine{compactFunction: func(_ context.Context, sink agent.EventSink) error {
		if err := sink(agent.Event{Kind: agent.EventCompactionStart}); err != nil {
			return err
		}
		return sink(agent.Event{Kind: agent.EventCompactionEnd, Usage: agent.Usage{TotalTokens: 100}})
	}}
	messages := make(chan engineMessage, 3)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	model.appendBlock(blockAssistant, "existing conversation")
	model.contextTokens = 90
	if err := model.insertInput("/compact"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !model.running || model.activity.kind != activityCompacting {
		t.Fatalf("running=%v activity=%+v", model.running, model.activity)
	}
	for range 3 {
		select {
		case message := <-messages:
			if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: message}); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("compaction did not complete")
		}
	}
	if engine.compactionCount() != 1 || model.running || model.activity.kind != activityReady || model.contextTokens != 0 {
		t.Fatalf("compactions=%d running=%v activity=%+v context=%d", engine.compactionCount(), model.running, model.activity, model.contextTokens)
	}
	if len(model.blocks) != 2 || model.blocks[0].text != "existing conversation" || model.blocks[1].kind != blockContext || model.blocks[1].text != "Compacting conversation" {
		t.Fatalf("blocks = %+v", model.blocks)
	}
}

func TestTUIControllerSetsShowsAndClearsGoal(t *testing.T) {
	engine := &fakeEngine{}
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionShowGoal}); err != nil {
		t.Fatal(err)
	}
	if len(model.blocks) != 1 || model.blocks[0].text != "No goal is set" {
		t.Fatalf("blocks = %+v", model.blocks)
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionSetGoal, prompt: "finish migration"}); err != nil {
		t.Fatal(err)
	}
	goal, ok := engine.Goal()
	if !ok || goal.Objective != "finish migration" || !model.running {
		t.Fatalf("goal=%+v exists=%v running=%v", goal, ok, model.running)
	}
	select {
	case <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("goal turn did not complete")
	}
	calls := engine.snapshot()
	if !slices.Equal(calls, []string{"finish migration"}) {
		t.Fatalf("calls = %q", calls)
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionShowGoal}); err != nil {
		t.Fatal(err)
	}
	if model.blocks[len(model.blocks)-1].text != "Goal: finish migration" {
		t.Fatalf("goal status block = %+v", model.blocks[len(model.blocks)-1])
	}

	if _, err := controller.applyAction(ctx, tuiAction{kind: tuiActionClearGoal}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok || model.blocks[len(model.blocks)-1].text != "Goal cleared" {
		t.Fatalf("goal still set or wrong block: %+v", model.blocks[len(model.blocks)-1])
	}
}

func TestTUIControllerClearsGoalWhileRunning(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "finish migration"}}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	if err := model.insertInput("/goal clear"); err != nil {
		t.Fatal(err)
	}
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.Goal(); ok {
		t.Fatal("goal survived running clear command")
	}
	last := model.blocks[len(model.blocks)-1]
	if !model.running || last.kind != blockInfo || last.text != "Goal cleared" {
		t.Fatalf("running=%v block=%+v", model.running, last)
	}
}

func TestTUIControllerDoesNotStartGoalWhenSetFails(t *testing.T) {
	setErr := errors.New("invalid goal")
	engine := &fakeEngine{setGoalErr: setErr}
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if _, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionSetGoal, prompt: "goal"}); err != nil {
		t.Fatal(err)
	}
	calls := engine.snapshot()
	if len(calls) != 0 || model.running || model.activity.kind != activityError || model.activity.detail != setErr.Error() {
		t.Fatalf("calls=%q running=%v activity=%+v", calls, model.running, model.activity)
	}
}

func TestTUIControllerNewSessionLeavesCurrentGoal(t *testing.T) {
	engine := &fakeEngine{goal: &agent.GoalState{Objective: "goal"}}
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	_, err := controller.applyAction(context.Background(), tuiAction{kind: tuiActionNewSession})
	var request *NewSessionRequest
	if !errors.As(err, &request) {
		t.Fatalf("new session error = %v", err)
	}
	if _, ok := engine.Goal(); !ok {
		t.Fatal("current goal was cleared")
	}
}

func TestTUIControllerAppliesSubagentStatus(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	controller := tuiController{model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard}

	_, err := controller.transition(context.Background(), tuiEvent{
		kind:           tuiEventSubagentStatus,
		subagentStatus: agent.SubagentStatus{Running: 2, Finalizing: 1, Completed: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.subagentStatus != (agent.SubagentStatus{Running: 2, Finalizing: 1, Completed: 1}) || !controller.dirty {
		t.Fatalf("status=%+v dirty=%v", model.subagentStatus, controller.dirty)
	}

	_, err = controller.transition(context.Background(), tuiEvent{
		kind:           tuiEventSubagentStatus,
		subagentStatus: agent.SubagentStatus{Running: -1, Finalizing: -1, Completed: -1},
	})
	if err != nil || model.subagentStatus != (agent.SubagentStatus{}) {
		t.Fatalf("sanitized status=%+v error=%v", model.subagentStatus, err)
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

func TestTUIControllerQueuesAndDequeuesSteering(t *testing.T) {
	var queued []string
	engine := &fakeEngine{
		steerFunction: func(prompt string) bool {
			queued = append(queued, prompt)
			return true
		},
		clearFunction: func() []string {
			messages := append([]string(nil), queued...)
			queued = nil
			return messages
		},
	}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	if err := model.insertInput("steer"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(queued, []string{"steer"}) || !slices.Equal(model.steering, []string{"steer"}) {
		t.Fatalf("engine queue=%q model queue=%q", queued, model.steering)
	}
	if calls := engine.snapshot(); len(calls) != 0 {
		t.Fatalf("steering started runs: %q", calls)
	}

	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyAltUp}}); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 || len(model.steering) != 0 || string(model.input) != "steer\n\ndraft" {
		t.Fatalf("queue=%q model queue=%q input=%q", queued, model.steering, model.input)
	}
}

func TestTUIControllerCancelRestoresQueuedAndDeferredSteering(t *testing.T) {
	engine := &fakeEngine{clearFunction: func() []string { return []string{"accepted"} }}
	model := newTUIModel(80, 24, Options{})
	model.running = true
	model.queueSteering("accepted")
	model.queueSteering("deferred")
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	canceled := false
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
		deferredSteering: []string{"deferred"},
		turnCancel:       func() { canceled = true },
	}

	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEscape}}); err != nil {
		t.Fatal(err)
	}
	if !canceled || len(controller.deferredSteering) != 0 || len(model.steering) != 0 {
		t.Fatalf("canceled=%v deferred=%q pending=%q", canceled, controller.deferredSteering, model.steering)
	}
	if string(model.input) != "accepted\n\ndeferred\n\ndraft" {
		t.Fatalf("restored input = %q", model.input)
	}
}

func TestTUIControllerRunsRejectedSteeringSequentially(t *testing.T) {
	steerCalls := 0
	engine := &fakeEngine{
		steerFunction: func(string) bool {
			steerCalls++
			return false
		},
	}
	messages := make(chan engineMessage, 4)
	stopped := make(chan struct{})
	defer close(stopped)
	model := newTUIModel(80, 24, Options{})
	model.running = true
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: engine, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	ctx := context.Background()

	for _, prompt := range []string{"one", "two"} {
		if err := model.insertInput(prompt); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keyEvent{code: keyEnter}}); err != nil {
			t.Fatal(err)
		}
	}
	if steerCalls != 1 || !slices.Equal(controller.deferredSteering, []string{"one", "two"}) {
		t.Fatalf("steer calls=%d deferred=%q", steerCalls, controller.deferredSteering)
	}

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: engineMessage{done: true}}); err != nil {
		t.Fatal(err)
	}
	if !model.running || !slices.Equal(controller.deferredSteering, []string{"two"}) {
		t.Fatalf("first replay running=%v deferred=%q", model.running, controller.deferredSteering)
	}
	var firstDone engineMessage
	select {
	case firstDone = <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("first deferred turn did not complete")
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: firstDone}); err != nil {
		t.Fatal(err)
	}
	var secondDone engineMessage
	select {
	case secondDone = <-messages:
	case <-time.After(2 * time.Second):
		t.Fatal("second deferred turn did not complete")
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventEngine, engine: secondDone}); err != nil {
		t.Fatal(err)
	}
	calls := engine.snapshot()
	if !slices.Equal(calls, []string{"one", "two"}) || model.running || len(model.steering) != 0 {
		t.Fatalf("calls=%q running=%v pending=%q", calls, model.running, model.steering)
	}
}

func TestTUIControllerRestoresSteeringAfterRunError(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.running = true
	model.queueSteering("retry this")
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, engine: &fakeEngine{}, output: io.Discard,
		engineMessages: make(chan engineMessage, 1), stopped: make(chan struct{}),
	}

	failure := errors.New("failed")
	if _, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventEngine, engine: engineMessage{done: true, err: failure}}); err != nil {
		t.Fatal(err)
	}
	if string(model.input) != "retry this" || len(model.steering) != 0 || model.activity.kind != activityError {
		t.Fatalf("input=%q steering=%q activity=%+v", model.input, model.steering, model.activity)
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
