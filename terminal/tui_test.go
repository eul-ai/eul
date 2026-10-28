package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
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
	_, err := runTest(ctx, strings.NewReader(""), &output, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runTest() error = %v", err)
	}
}

func TestRunTUIParentCancellationWinsAfterActiveEOF(t *testing.T) {
	fake := newFakeSession()
	fake.calls = make(chan string, 2)
	reader, writer := io.Pipe()
	defer reader.Close()
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runTest(ctx, reader, &output, Options{Session: fake})
		done <- err
	}()
	if _, err := io.WriteString(writer, "wait\r"); err != nil {
		t.Fatal(err)
	}
	if call := <-fake.calls; call != "start" {
		t.Fatalf("call = %q", call)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if call := <-fake.calls; call != "interrupt" {
		t.Fatalf("call = %q", call)
	}
	cancel()
	fake.updates <- fake.settle(context.Canceled)

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
	fake := newFakeSession()
	output := newSignalingWriter("subagent-1")

	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), reader, output, Options{Session: fake})
		done <- err
	}()

	fake.updates <- session.Update{Kind: session.UpdateSubagents, Subagents: subagent.Status{
		Active: []subagent.JobStatus{{ID: "subagent-1", Description: "inspect", State: subagent.StateRunning}},
	}}
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
	fake := newFakeSession()
	fake.calls = make(chan string, 1)
	options := Options{
		Session: fake,
		Services: Services{LoadUsage: func(context.Context) (backend.AccountUsage, error) {
			calls <- struct{}{}
			return backend.AccountUsage{Windows: []backend.UsageWindow{{Duration: 5 * time.Hour, UsedPercent: 25}}}, nil
		}},
	}

	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), reader, output, options)
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
	<-fake.calls
	fake.updates <- fake.settle(nil)
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
		_, err := runTest(context.Background(), reader, io.Discard, Options{
			Services: Services{LoadUsage: func(ctx context.Context) (backend.AccountUsage, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return backend.AccountUsage{}, ctx.Err()
			}},
		})
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
	_, err := runTest(context.Background(), terminalErrorReader{err: readErr}, &output, Options{})
	if !errors.Is(err, readErr) {
		t.Fatalf("runTest() error = %v", err)
	}
}

func TestHandleKeyRunsTurnAndAppliesOrderedEvents(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	if err := model.insertInput("hello"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)
	ctx := context.Background()

	exit, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}})
	if err != nil || exit || !model.running || !slices.Equal(contentTexts(testSession(model).sent), []string{"hello"}) {
		t.Fatalf("enter exit=%v err=%v running=%v sent=%+v", exit, err, model.running, testSession(model).sent)
	}

	fake := testSession(model)
	for _, update := range []session.Update{
		fake.event(agent.Event{Kind: agent.EventAssistantText, Text: "answer"}),
		fake.event(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 42}}),
		fake.settle(nil),
	} {
		if _, err := controller.transition(ctx, sessionEvent(update)); err != nil {
			t.Fatal(err)
		}
	}
	if model.running || model.activity.kind != activityReady || model.contextTokens != 42 {
		t.Fatalf("model after turn = %+v", model)
	}
	if len(model.entries) != 2 || model.entries[0].Kind != session.EntryUser || model.entries[1].Text != "answer" {
		t.Fatalf("blocks = %+v", model.entries)
	}
}

func TestHandleKeyCancelsIncompleteToolTurnWithoutResettingConversation(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	model.contextTokens = 123
	if err := model.insertInput("wait"); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(model)
	ctx := context.Background()
	fake := testSession(model)

	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []session.Update{
		fake.event(agent.Event{Kind: agent.EventContextUsage, Usage: agent.Usage{TotalTokens: 456}}),
		fake.event(agent.Event{Kind: agent.EventToolExecute, Call: agent.ToolCall{ID: "call-1", Name: "write"}}),
	} {
		if _, err := controller.transition(ctx, sessionEvent(update)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.CtrlC}}); err != nil {
		t.Fatal(err)
	}
	if model.activity.kind != activityCanceling || !fake.interrupted {
		t.Fatalf("activity = %+v, interrupted = %v", model.activity, fake.interrupted)
	}

	if _, err := controller.transition(ctx, sessionEvent(fake.settle(context.Canceled))); err != nil {
		t.Fatal(err)
	}
	last := model.entries[len(model.entries)-1]
	if model.contextTokens != 456 || model.activity.kind != activityReady || last.Kind != session.EntryInfo {
		t.Fatalf("context=%d activity=%+v blocks=%+v", model.contextTokens, model.activity, model.entries)
	}
}

func runTest(ctx context.Context, input io.Reader, output io.Writer, options Options) (RunOutcome, error) {
	if options.Session == nil {
		options.Session = newFakeSession()
	}
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
	model := newTestModel(80, 24, Options{})
	if err := model.insertInput("draft"); err != nil {
		t.Fatal(err)
	}

	exit, err := handleKey(context.Background(), model, keys.Event{Code: keys.CtrlC})
	if err != nil || exit || len(model.input) != 0 {
		t.Fatalf("first Ctrl-C: exit=%v err=%v input=%q", exit, err, model.inputText())
	}
	exit, err = handleKey(context.Background(), model, keys.Event{Code: keys.CtrlC})
	if exit || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("second Ctrl-C: exit=%v err=%v", exit, err)
	}
}

func TestHandleKeyShiftTabCyclesThinkingLevel(t *testing.T) {
	var configured agent.ThinkingLevel
	model := newTestModel(80, 24, Options{
		Config: Config{ThinkingLevel: agent.ThinkingMedium},
		Controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}},
	})
	stopped := make(chan struct{})
	defer close(stopped)

	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: Controls{SetThinkingLevel: func(level agent.ThinkingLevel) error {
			configured = level
			return nil
		}}, output: io.Discard,
		stopped: stopped,
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
	model := newTestModel(80, 24, Options{})
	stopped := make(chan struct{})
	defer close(stopped)
	controller := tuiController{
		model: model, renderer: &tuiRenderer{}, controls: controlsFor(&fakeEngine{}), output: io.Discard,
		stopped: stopped,
	}

	exit, err := controller.transition(context.Background(), tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.CtrlL}})
	if err != nil || exit || !controller.forceRedraw {
		t.Fatalf("exit=%v err=%v forceRedraw=%v", exit, err, controller.forceRedraw)
	}
}

func TestHandleKeyCommands(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	for _, command := range []string{"/help", "/unknown"} {
		if err := model.insertInput(command); err != nil {
			t.Fatal(err)
		}
		if exit, err := handleKey(context.Background(), model, keys.Event{Code: keys.Enter}); err != nil || exit {
			t.Fatalf("command %q exit=%v err=%v", command, exit, err)
		}
	}
	if len(model.entries) != 2 || model.entries[0].Kind != session.EntryInfo || model.entries[1].Kind != session.EntryError {
		t.Fatalf("blocks = %+v", model.entries)
	}

	if err := model.insertInput("/exit"); err != nil {
		t.Fatal(err)
	}
	if exit, err := handleKey(context.Background(), model, keys.Event{Code: keys.Enter}); err != nil || !exit {
		t.Fatalf("exit command exit=%v err=%v", exit, err)
	}

	if err := model.insertInput("/new"); err != nil {
		t.Fatal(err)
	}
	exit, err := handleKey(context.Background(), model, keys.Event{Code: keys.Enter})
	if err != nil || !exit {
		t.Fatalf("new command exit=%v err=%v", exit, err)
	}
	if len(model.entries) != 2 {
		t.Fatalf("blocks=%+v", model.entries)
	}
}

func TestRunTUISubmitsInitialPrompt(t *testing.T) {
	fake := newFakeSession()
	fake.calls = make(chan string, 2)
	options := Options{Session: fake}
	options.Config.InitialPrompt = "investigate the store"
	done := make(chan error, 1)
	go func() {
		_, err := runTest(context.Background(), strings.NewReader(""), io.Discard, options)
		done <- err
	}()

	if call := <-fake.calls; call != "start" {
		t.Fatalf("call = %q", call)
	}
	fake.updates <- fake.settle(nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sent := contentTexts(fake.sent); !slices.Equal(sent, []string{"investigate the store"}) {
		t.Fatalf("sent = %q", sent)
	}
}
