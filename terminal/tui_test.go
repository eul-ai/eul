package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/keys"
)

func TestScreenModesRestoreEnhancedKeyboardReporting(t *testing.T) {
	if !strings.Contains(enterScreen, "\x1b[>1u") || !strings.Contains(leaveScreen, "\x1b[<u") {
		t.Fatalf("enter=%q leave=%q", enterScreen, leaveScreen)
	}
}

func TestSetTerminalTitleUsesWorkingDirectoryName(t *testing.T) {
	var output bytes.Buffer
	if err := setTerminalTitle(&output, filepath.Join("home", "daniel", "Code", "eul")); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.HasPrefix(got, "\x1b]2;") || !strings.Contains(got, filepath.Base(filepath.Join("home", "daniel", "Code", "eul"))) || !strings.HasSuffix(got, "\x07") {
		t.Fatalf("terminal title = %q", got)
	}
}

func TestRunTUIParentCancellationWinsOverEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	_, err := runTest(ctx, strings.NewReader(""), &output, optionsForEngine(&fakeEngine{}, Options{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runTest() error = %v", err)
	}
}

func TestRunTUIParentCancellationWinsAfterActiveEOF(t *testing.T) {
	started := make(chan struct{})
	turnCanceled := make(chan struct{})
	release := make(chan struct{})
	engine := &fakeEngine{runContentFunction: func(ctx context.Context, _ []agent.ContentPart, _ agent.EventSink) (agent.RunResult, error) {
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
		_, err := runTest(ctx, reader, &output, optionsForEngine(engine, Options{}))
		done <- err
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
			t.Fatalf("runTest() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runTest() did not return")
	}
}

func TestRunTUIRendersSubagentUpdatesWhileIdle(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	manager := subagent.NewManager(func(ctx context.Context, _ subagent.Task, _ func(agent.Usage)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, subagent.Checkpoint{})
	defer manager.Close()
	output := newSignalingWriter("subagent-1")

	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), reader, output, optionsForEngine(&fakeEngine{}, Options{
			Events: Events{Subagents: manager},
		}))
		done <- err
	}()

	if _, err := manager.Start([]subagent.Task{{Description: "inspect", Prompt: "inspect"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("subagent status was not rendered")
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
		t.Fatal("runTest did not stop")
	}
}

func TestRunTUILoadsProviderUsageAtStartupAndAfterTurn(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	calls := make(chan struct{}, 3)
	output := newSignalingWriter("usage 25%")
	options := Options{
		Services: Services{LoadUsage: func(context.Context) (ProviderUsage, error) {
			calls <- struct{}{}
			return ProviderUsage{Windows: []UsageWindow{{Duration: 5 * time.Hour, UsedPercent: 25}}}, nil
		}},
	}

	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), reader, output, optionsForEngine(&fakeEngine{}, options))
		done <- err
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
		t.Fatal("runTest did not stop")
	}
}

func TestRunTUIWaitsForProviderUsageCleanup(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), reader, io.Discard, optionsForEngine(&fakeEngine{}, Options{
			Services: Services{LoadUsage: func(ctx context.Context) (ProviderUsage, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return ProviderUsage{}, ctx.Err()
			}},
		}))
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider usage request did not start")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("provider usage request was not canceled")
	}
	select {
	case err := <-done:
		t.Fatalf("runTest returned before usage cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runTest did not return after usage cleanup")
	}
}

func TestRunTUIReturnsInputReadFailure(t *testing.T) {
	readErr := errors.New("read failed")
	var output bytes.Buffer
	_, err := runTest(context.Background(), terminalErrorReader{err: readErr}, &output, optionsForEngine(&fakeEngine{}, Options{}))
	if !errors.Is(err, readErr) {
		t.Fatalf("runTest() error = %v", err)
	}
}

func TestHandleKeyRunsTurnAndAppliesOrderedEvents(t *testing.T) {
	engine := &fakeEngine{runContentFunction: func(_ context.Context, content []agent.ContentPart, sink agent.EventSink) (agent.RunResult, error) {
		if prompt := contentText(content); prompt != "hello" {
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

	exit, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.Enter}, messages, stopped, &cancel)
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
	engine.runContentFunction = func(ctx context.Context, _ []agent.ContentPart, sink agent.EventSink) (agent.RunResult, error) {
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

	if _, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.Enter}, messages, stopped, &cancel); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.CtrlC}, messages, stopped, &cancel); err != nil {
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
			last := model.blocks[len(model.blocks)-1]
			if model.contextTokens != 456 || model.activity.kind != activityReady || last.kind != blockInfo {
				t.Fatalf("context=%d activity=%+v blocks=%+v", model.contextTokens, model.activity, model.blocks)
			}
			return
		case <-timeout:
			t.Fatal("canceled turn did not complete")
		}
	}
}

func runTest(ctx context.Context, input io.Reader, output io.Writer, options Options) (RunOutcome, error) {
	runner := &Runner{output: output, outputFD: -1, keys: make(chan keys.Event, 64), stopped: make(chan struct{})}
	defer close(runner.stopped)
	go keys.Read(input, runner.keys, runner.stopped)
	return runner.run(ctx, options, 80, 24)
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

	exit, err := handleKey(context.Background(), model, &fakeEngine{}, keys.Event{Code: keys.CtrlC}, messages, stopped, &cancel)
	if err != nil || exit || len(model.input) != 0 {
		t.Fatalf("first Ctrl-C: exit=%v err=%v input=%q", exit, err, model.inputText())
	}
	exit, err = handleKey(context.Background(), model, &fakeEngine{}, keys.Event{Code: keys.CtrlC}, messages, stopped, &cancel)
	if exit || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("second Ctrl-C: exit=%v err=%v", exit, err)
	}
}

func TestHandleKeyShiftTabCyclesThinkingLevel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTUIModel(80, 24, Options{
		Config: Config{ThinkingLevel: agent.ThinkingMedium},
		Controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}},
	})
	messages := make(chan engineMessage, 1)
	stopped := make(chan struct{})
	defer close(stopped)

	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, operations: operationsFor(&fakeEngine{}), controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}}, output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}
	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.ShiftTab}})
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
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, operations: operationsFor(&fakeEngine{}), controls: controlsFor(&fakeEngine{}), output: io.Discard,
		engineMessages: messages, stopped: stopped,
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.CtrlL}})
	if err != nil || exit || !controller.forceRedraw {
		t.Fatalf("exit=%v err=%v forceRedraw=%v", exit, err, controller.forceRedraw)
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
		if exit, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.Enter}, messages, stopped, &cancel); err != nil || exit {
			t.Fatalf("command %q exit=%v err=%v", command, exit, err)
		}
	}
	if len(model.blocks) != 2 || model.blocks[0].kind != blockInfo || model.blocks[1].kind != blockError {
		t.Fatalf("blocks = %+v", model.blocks)
	}

	if err := model.insertInput("/exit"); err != nil {
		t.Fatal(err)
	}
	if exit, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.Enter}, messages, stopped, &cancel); err != nil || !exit {
		t.Fatalf("exit command exit=%v err=%v", exit, err)
	}

	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}
	exit, err := handleKey(context.Background(), model, engine, keys.Event{Code: keys.Enter}, messages, stopped, &cancel)
	if err != nil || !exit {
		t.Fatalf("new command exit=%v err=%v", exit, err)
	}
	if len(model.blocks) != 2 {
		t.Fatalf("blocks=%+v", model.blocks)
	}
}

func TestRunTUISubmitsInitialPrompt(t *testing.T) {
	submitted := make(chan string, 1)
	engine := &fakeEngine{runContentFunction: func(_ context.Context, content []agent.ContentPart, _ agent.EventSink) (agent.RunResult, error) {
		submitted <- contentText(content)
		return agent.RunResult{}, nil
	}}

	var output bytes.Buffer
	options := optionsForEngine(engine, Options{})
	options.Config.InitialPrompt = "investigate the store"
	if _, err := runTest(context.Background(), strings.NewReader(""), &output, options); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-submitted:
		if got != "investigate the store" {
			t.Fatalf("initial prompt = %q", got)
		}
	default:
		t.Fatal("the initial prompt was not submitted")
	}
}
