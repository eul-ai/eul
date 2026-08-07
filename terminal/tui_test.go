package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
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

func TestRunTUILoadsProviderUsageAtStartupAndAfterTurn(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	calls := make(chan struct{}, 3)
	output := newSignalingWriter("5h limit 75% left")
	options := Options{
		Input:  reader,
		Output: output,
		LoadUsage: func(context.Context) (agent.ProviderUsage, error) {
			calls <- struct{}{}
			return agent.ProviderUsage{Windows: []agent.UsageWindow{{Duration: 5 * time.Hour, UsedPercent: 25}}}, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runTUI(context.Background(), &fakeEngine{}, options, -1, 80, 24)
	}()

	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("startup usage request did not start")
	}
	select {
	case <-output.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("startup usage was not rendered")
	}
	if _, err := io.WriteString(writer, "hello\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("post-turn usage request did not start")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runTUI did not stop")
	}
}

func TestLoadProviderUsageCoalescesRequestsAndRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan struct{}, 1)
	messages := make(chan providerUsageMessage, 1)
	started := make(chan int, 3)
	releaseFirst := make(chan struct{})
	calls := 0
	load := func(context.Context) (agent.ProviderUsage, error) {
		calls++
		started <- calls
		if calls == 1 {
			<-releaseFirst
			return agent.ProviderUsage{}, errors.New("temporarily unavailable")
		}
		return agent.ProviderUsage{Windows: []agent.UsageWindow{{Duration: 7 * 24 * time.Hour, UsedPercent: 20}}}, nil
	}
	go loadProviderUsage(ctx, load, requests, messages)

	requestProviderUsage(requests)
	if call := <-started; call != 1 {
		t.Fatalf("first call = %d", call)
	}
	requestProviderUsage(requests)
	requestProviderUsage(requests)
	requestProviderUsage(requests)
	close(releaseFirst)
	if message := <-messages; message.err == nil {
		t.Fatal("first usage request unexpectedly succeeded")
	}
	if call := <-started; call != 2 {
		t.Fatalf("second call = %d", call)
	}
	message := <-messages
	if message.err != nil || len(message.usage.Windows) != 1 || message.usage.Windows[0].UsedPercent != 20 {
		t.Fatalf("second usage result = %+v", message)
	}

	select {
	case call := <-started:
		t.Fatalf("unexpected coalesced call %d", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLoadProviderUsageCancelsActiveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan struct{}, 1)
	messages := make(chan providerUsageMessage, 1)
	started := make(chan struct{})
	canceled := make(chan error, 1)
	load := func(ctx context.Context) (agent.ProviderUsage, error) {
		close(started)
		<-ctx.Done()
		canceled <- ctx.Err()
		return agent.ProviderUsage{}, ctx.Err()
	}
	go loadProviderUsage(ctx, load, requests, messages)

	requestProviderUsage(requests)
	<-started
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("load error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active usage request was not canceled")
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

type signalingWriter struct {
	mu    sync.Mutex
	text  string
	match string
	seen  chan struct{}
}

func newSignalingWriter(match string) *signalingWriter {
	return &signalingWriter{match: match, seen: make(chan struct{}, 1)}
}

func (writer *signalingWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.text += string(content)
	if strings.Contains(writer.text, writer.match) {
		select {
		case writer.seen <- struct{}{}:
		default:
		}
	}
	return len(content), nil
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

	if err := model.insertInput("/clear"); err != nil {
		t.Fatal(err)
	}
	if exit, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel); err != nil || exit {
		t.Fatalf("clear command exit=%v err=%v", exit, err)
	}
	if _, resets := engine.snapshot(); resets != 1 || len(model.blocks) != 0 || model.activity.kind != activityReady {
		t.Fatalf("resets=%d blocks=%+v activity=%+v", resets, model.blocks, model.activity)
	}

	if err := model.insertInput("/exit"); err != nil {
		t.Fatal(err)
	}
	if exit, err := handleKey(context.Background(), model, engine, keyEvent{code: keyEnter}, messages, stopped, &cancel); err != nil || !exit {
		t.Fatalf("exit command exit=%v err=%v", exit, err)
	}
}
