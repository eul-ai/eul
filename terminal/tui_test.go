package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"yaah/agent"
)

func TestScreenModesRestoreEnhancedKeyboardReporting(t *testing.T) {
	if !strings.Contains(enterScreen, "\x1b[>1u") || !strings.Contains(leaveScreen, "\x1b[<u") {
		t.Fatalf("enter=%q leave=%q", enterScreen, leaveScreen)
	}
}

func TestRunTUIParentCancellationWinsOverEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := runTUI(ctx, &fakeEngine{}, Options{Input: strings.NewReader(""), Output: &output}, -1, 80, 24)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func TestRunTUIParentCancellationWinsAfterActiveEOF(t *testing.T) {
	started := make(chan struct{})
	turnCanceled := make(chan struct{})
	release := make(chan struct{})
	engine := &fakeEngine{runFunction: func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
		close(started)
		<-ctx.Done()
		close(turnCanceled)
		<-release
		return agent.RunResult{}, ctx.Err()
	}}
	reader, writer := io.Pipe()
	defer reader.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runTUI(ctx, engine, Options{Input: reader, Output: &output}, -1, 80, 24)
	}()
	if _, err := io.WriteString(writer, "wait\r"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	<-turnCanceled
	cancel()
	close(release)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runTUI() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runTUI() did not return")
	}
}

func TestRunTUIReturnsInputReadFailure(t *testing.T) {
	readErr := errors.New("read failed")
	var output bytes.Buffer
	err := runTUI(context.Background(), &fakeEngine{}, Options{
		Input: terminalErrorReader{err: readErr}, Output: &output,
	}, -1, 80, 24)
	if !errors.Is(err, readErr) {
		t.Fatalf("runTUI() error = %v", err)
	}
}

func TestHandleKeyRunsTurnAndAppliesOrderedEvents(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, prompt string, sink agent.EventSink) (agent.RunResult, error) {
		if prompt != "hello" {
			t.Fatalf("prompt = %q", prompt)
		}
		if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: "answer"}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 42}}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{Text: "answer"}, nil
	}}
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("hello"); err != nil {
		t.Fatal(err)
	}
	messages := make(chan engineMessage, 10)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	exit, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel)
	if err != nil || exit || !model.running {
		t.Fatalf("handleKey() exit=%v err=%v running=%v", exit, err, model.running)
	}

	for {
		select {
		case message := <-messages:
			if message.done {
				cancel()
				model.finishTurn(message.err)
				if model.running || model.activity.kind != activityReady || model.contextTokens != 42 {
					t.Fatalf("model after turn = %+v", model)
				}
				if len(model.blocks) != 2 || model.blocks[0].kind != blockUser || model.blocks[1].text != "answer" {
					t.Fatalf("blocks = %+v", model.blocks)
				}
				return
			}
			model.applyAgentEvent(*message.event)
		case <-time.After(2 * time.Second):
			t.Fatal("turn did not complete")
		}
	}
}

func TestHandleKeyCancelsIncompleteToolTurnWithoutResettingConversation(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeEngine{}
	engine.runFunction = func(ctx context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 456}}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventToolExecute, Call: agent.ToolCall{ID: "call-1", Name: "write"}}); err != nil {
			return agent.RunResult{}, err
		}
		close(started)
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	}
	model := newTUIModel(80, 24, Options{})
	model.contextTokens = 123
	if err := model.insertInput("wait"); err != nil {
		t.Fatal(err)
	}
	messages := make(chan engineMessage, 10)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	if _, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := handleKey(context.Background(), model, engine, keyEvent{code: keyCtrlC}, messages, stopped, &cancel); err != nil {
		t.Fatal(err)
	}
	if model.activity.kind != activityCanceling {
		t.Fatalf("activity = %+v", model.activity)
	}

	timeout := time.After(2 * time.Second)
	for {
		select {
		case message := <-messages:
			if !message.done {
				model.applyAgentEvent(*message.event)
				continue
			}
			model.finishTurn(message.err)
			_, resets := engine.snapshot()
			last := model.blocks[len(model.blocks)-1].text
			if resets != 0 || model.contextTokens != 456 || model.activity.kind != activityReady || !strings.Contains(last, "tool side effects may remain") || strings.Contains(last, "cleared") {
				t.Fatalf("resets=%d context=%d activity=%+v blocks=%+v", resets, model.contextTokens, model.activity, model.blocks)
			}
			return
		case <-timeout:
			t.Fatal("canceled turn did not complete")
		}
	}
}

type terminalErrorReader struct {
	err error
}

func (r terminalErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestHandleKeyCtrlCClearsInputBeforeExiting(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	exit, err := handleKey(context.Background(), model, &fakeEngine{}, keyEvent{code: keyCtrlC}, messages, stopped, &cancel)
	if err != nil || exit || len(model.input) != 0 {
		t.Fatalf("first Ctrl-C: exit=%v err=%v input=%q", exit, err, model.input)
	}
	exit, err = handleKey(context.Background(), model, &fakeEngine{}, keyEvent{code: keyCtrlC}, messages, stopped, &cancel)
	if exit || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("second Ctrl-C: exit=%v err=%v", exit, err)
	}
}

func TestHandleKeyShiftTabCyclesThinkingLevel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTUIModel(80, 24, Options{
		ThinkingLevel: agent.ThinkingMedium,
		SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		},
	})
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	exit, err := handleKey(context.Background(), model, &fakeEngine{}, keyEvent{code: keyShiftTab}, messages, stopped, &cancel)
	if err != nil || exit || model.thinkingLevel != agent.ThinkingHigh || configured != agent.ThinkingHigh {
		t.Fatalf("exit=%v err=%v thinking=%q configured=%q", exit, err, model.thinkingLevel, configured)
	}
	if frame := renderFrame(model); !strings.Contains(frame, ansiColors(currentTheme.thinkingColor(agent.ThinkingHigh), terminalColor{}, false)) {
		t.Fatalf("cycled thinking color missing from frame: %q", frame)
	}
}

func TestHandleKeyCtrlLRequestsFullRedraw(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	exit, err := handleKey(context.Background(), model, &fakeEngine{}, keyEvent{code: keyCtrlL}, messages, stopped, &cancel)
	if err != nil || exit || !model.forceRedraw {
		t.Fatalf("exit=%v err=%v forceRedraw=%v", exit, err, model.forceRedraw)
	}
}

func TestHandleKeyCommands(t *testing.T) {
	engine := &fakeEngine{}
	model := newTUIModel(80, 24, Options{})
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)
	var cancel context.CancelFunc

	for _, command := range []string{"/help", "/unknown"} {
		if err := model.insertInput(command); err != nil {
			t.Fatal(err)
		}
		if exit, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel); err != nil || exit {
			t.Fatalf("command %q exit=%v err=%v", command, exit, err)
		}
	}
	if len(model.blocks) != 2 || model.blocks[0].kind != blockInfo || model.blocks[1].kind != blockError {
		t.Fatalf("blocks = %+v", model.blocks)
	}

	if err := model.insertInput("/exit"); err != nil {
		t.Fatal(err)
	}
	if exit, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel); err != nil || !exit {
		t.Fatalf("exit command exit=%v err=%v", exit, err)
	}
}
