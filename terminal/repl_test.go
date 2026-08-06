package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yaah/agent"
)

type fakeEngine struct {
	mu          sync.Mutex
	calls       []string
	resets      int
	needsReset  bool
	runFunction func(context.Context, string, agent.EventSink) (agent.RunResult, error)
}

func (e *fakeEngine) Run(ctx context.Context, prompt string, sink agent.EventSink) (agent.RunResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, prompt)
	function := e.runFunction
	e.mu.Unlock()
	if function == nil {
		return agent.RunResult{}, nil
	}
	return function(ctx, prompt, sink)
}

func (e *fakeEngine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resets++
	e.needsReset = false
}

func (e *fakeEngine) NeedsReset() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.needsReset
}

func (e *fakeEngine) setNeedsReset(value bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.needsReset = value
}

func (e *fakeEngine) snapshot() ([]string, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...), e.resets
}

func TestRunCommandsFinalLineAndEOF(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, prompt string, sink agent.EventSink) (agent.RunResult, error) {
		for _, delta := range []string{"ans", "wer"} {
			if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: delta}); err != nil {
				return agent.RunResult{}, err
			}
		}
		return agent.RunResult{Text: "answer", AssistantMessages: []string{"answer"}}, nil
	}}
	input := strings.NewReader("  \n/help\r\n/clear\nhello")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), engine, Options{
		Input:       input,
		Output:      &stdout,
		ErrorOutput: &stderr,
		Model:       "model",
		CWD:         "/project",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls, resets := engine.snapshot()
	if len(calls) != 1 || calls[0] != "hello" || resets != 1 {
		t.Fatalf("calls = %v, resets = %d", calls, resets)
	}
	if strings.Count(stdout.String(), "answer") != 1 || !strings.Contains(stdout.String(), "Commands:") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, want := range []string{"yaah · openai/model · /project", "[conversation cleared]", "> "} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

func TestRunExitAndEmptyEOFDoNotRunEngine(t *testing.T) {
	for _, input := range []string{"/exit\n", ""} {
		engine := &fakeEngine{}
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), engine, Options{Input: strings.NewReader(input), Output: &stdout, ErrorOutput: &stderr})
		if err != nil {
			t.Fatalf("Run(%q) error = %v", input, err)
		}
		calls, _ := engine.snapshot()
		if len(calls) != 0 {
			t.Fatalf("Run(%q) calls = %v", input, calls)
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	engine := &fakeEngine{}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), engine, Options{Input: strings.NewReader("/unknown value\n/exit\n"), Output: &stdout, ErrorOutput: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "unknown command /unknown value") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	calls, _ := engine.snapshot()
	if len(calls) != 0 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRunOneShotRendersEventsOnce(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		events := []agent.Event{
			{Kind: agent.EventAssistantText, Text: "Checking"},
			{Kind: agent.EventToolStart, Call: agent.ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"file.txt","content":"value"}`)}},
			{Kind: agent.EventToolEnd, Result: agent.ToolResult{Tool: "write", IsError: true, Output: "write failed"}},
			{Kind: agent.EventAssistantText, Text: "Done"},
		}
		for _, event := range events {
			if err := sink(event); err != nil {
				return agent.RunResult{}, err
			}
		}
		return agent.RunResult{Text: "Done", AssistantMessages: []string{"Checking", "Done"}}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "prompt", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Checking\nDone\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[tool] write file.txt") || strings.Contains(stderr.String(), "content") || !strings.Contains(stderr.String(), "write — error") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRenderedOutputSanitizesControlsAndTruncatesDiagnostics(t *testing.T) {
	value := strings.Repeat("long-value", 30) + `"quoted`
	arguments, err := json.Marshal(map[string]string{"command": "printf " + value})
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: "safe\x1b[31m\rrewrite\a"}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{Name: "bash", Arguments: arguments}}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "prompt", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stdout.String()+stderr.String(), "\x1b\r\a") || !strings.Contains(stderr.String(), "...") {
		t.Fatalf("unsafe output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunOneShotSummarizesBashExit(t *testing.T) {
	engine := &fakeEngine{runFunction: func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		if err := sink(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}}); err != nil {
			return agent.RunResult{}, err
		}
		if err := sink(agent.Event{Kind: agent.EventToolEnd, Result: agent.ToolResult{Tool: "bash", IsError: true, Output: "failed\n[exit status: 1]"}}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}}
	var stdout, stderr bytes.Buffer
	if err := RunOneShot(context.Background(), engine, "test", Options{Output: &stdout, ErrorOutput: &stderr}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), `[tool] bash "go test ./..."`) || !strings.Contains(stderr.String(), "bash — exit status: 1") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunDiscardsOversizedAndInvalidLinesThenContinues(t *testing.T) {
	input := append([]byte("123456\n"), []byte{'b', 'a', 'd', 0, '\n'}...)
	input = append(input, []byte{0xff, '\n'}...)
	input = append(input, []byte("ok\n/exit\n")...)
	engine := &fakeEngine{}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), engine, Options{
		Input: inputReader{Reader: bytes.NewReader(input)}, Output: &stdout, ErrorOutput: &stderr, MaxInputBytes: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := engine.snapshot()
	if len(calls) != 1 || calls[0] != "ok" {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(stderr.String(), "exceeds 5 bytes") || strings.Count(stderr.String(), "valid UTF-8") != 2 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunAcceptsLinesAboveScannerLimit(t *testing.T) {
	prompt := strings.Repeat("x", 70*1024)
	engine := &fakeEngine{}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), engine, Options{
		Input: strings.NewReader(prompt + "\n/exit\n"), Output: &stdout, ErrorOutput: &stderr, MaxInputBytes: 80 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := engine.snapshot()
	if len(calls) != 1 || calls[0] != prompt {
		t.Fatalf("received %d calls with prompt length %d", len(calls), len(calls[0]))
	}
}

func TestRunActiveInterruptWaitsAndResetsOnlyWhenNeeded(t *testing.T) {
	for _, needsReset := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "reset"}[needsReset], func(t *testing.T) {
			started := make(chan struct{})
			engine := &fakeEngine{}
			engine.runFunction = func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
				close(started)
				engine.setNeedsReset(needsReset)
				<-ctx.Done()
				return agent.RunResult{}, ctx.Err()
			}
			interrupts := make(chan os.Signal, 1)
			var stdout, stderr bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- Run(context.Background(), engine, Options{
					Input: strings.NewReader("wait\n/exit\n"), Output: &stdout, ErrorOutput: &stderr, Interrupts: interrupts,
				})
			}()
			select {
			case <-started:
				interrupts <- os.Interrupt
			case <-time.After(2 * time.Second):
				t.Fatal("turn did not start")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run() did not finish")
			}
			_, resets := engine.snapshot()
			wantResets := 0
			if needsReset {
				wantResets = 1
			}
			if resets != wantResets || !strings.Contains(stderr.String(), "[interrupted") {
				t.Fatalf("resets = %d, stderr = %q", resets, stderr.String())
			}
		})
	}
}

func TestRunReturnsParentCancellationWithoutRenderingRecoverableError(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeEngine{}
	engine.runFunction = func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
		close(started)
		engine.setNeedsReset(true)
		<-ctx.Done()
		return agent.RunResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, engine, Options{Input: strings.NewReader("wait\n"), Output: &stdout, ErrorOutput: &stderr})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after parent cancellation")
	}
	_, resets := engine.snapshot()
	if resets != 1 || strings.Contains(stderr.String(), "error: context canceled") {
		t.Fatalf("resets=%d stderr=%q", resets, stderr.String())
	}
}

func TestRunIdleInterruptAndOneShotInterrupt(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		interrupts := make(chan os.Signal, 1)
		interrupts <- os.Interrupt
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), &fakeEngine{}, Options{Input: reader, Output: &stdout, ErrorOutput: &stderr, Interrupts: interrupts})
		reader.Close()
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("Run() error = %v", err)
		}
	})
	t.Run("one shot", func(t *testing.T) {
		started := make(chan struct{})
		engine := &fakeEngine{}
		engine.runFunction = func(ctx context.Context, _ string, _ agent.EventSink) (agent.RunResult, error) {
			close(started)
			engine.setNeedsReset(true)
			<-ctx.Done()
			return agent.RunResult{}, ctx.Err()
		}
		interrupts := make(chan os.Signal, 1)
		var stdout, stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- RunOneShot(context.Background(), engine, "wait", Options{Output: &stdout, ErrorOutput: &stderr, Interrupts: interrupts})
		}()
		<-started
		interrupts <- os.Interrupt
		err := <-done
		_, resets := engine.snapshot()
		if !errors.Is(err, ErrInterrupted) || resets != 1 {
			t.Fatalf("RunOneShot() error = %v, resets = %d", err, resets)
		}
	})
}

func TestRunErrorsResetIncompleteTurnAndContinue(t *testing.T) {
	engine := &fakeEngine{}
	call := 0
	engine.runFunction = func(_ context.Context, _ string, sink agent.EventSink) (agent.RunResult, error) {
		call++
		if call == 1 {
			engine.setNeedsReset(true)
			return agent.RunResult{}, errors.New("provider\nfailed\r\x1b with api-secret " + strings.Repeat("x", 1000))
		}
		if err := sink(agent.Event{Kind: agent.EventAssistantText, Text: "recovered"}); err != nil {
			return agent.RunResult{}, err
		}
		return agent.RunResult{}, nil
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), engine, Options{
		Input: strings.NewReader("first\nsecond\n/exit\n"), Output: &stdout, ErrorOutput: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, resets := engine.snapshot()
	if len(calls) != 2 || resets != 1 || !strings.Contains(stdout.String(), "recovered") || !strings.Contains(stderr.String(), "api-secret") || strings.ContainsAny(stderr.String(), "\r\x1b") {
		t.Fatalf("calls=%v resets=%d stdout=%q stderr=%q", calls, resets, stdout.String(), stderr.String())
	}
}

func TestReadInputStopsSendingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events, requests := readInput(ctx, strings.NewReader("one\ntwo\nthree\n"), 100)
	requests <- struct{}{}
	if event := <-events; event.line != "one" || event.err != nil {
		t.Fatalf("first event = %+v", event)
	}
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("input pump did not stop after cancellation")
		}
	}
}

func TestRunPropagatesOutputErrors(t *testing.T) {
	engine := &fakeEngine{}
	err := Run(context.Background(), engine, Options{Input: strings.NewReader(""), Output: io.Discard, ErrorOutput: failingWriter{}})
	if !errors.Is(err, ErrOutput) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestOptionsValidation(t *testing.T) {
	valid := Options{Input: strings.NewReader(""), Output: io.Discard, ErrorOutput: io.Discard}
	if _, err := prepare(context.Background(), nil, valid, true); err == nil {
		t.Fatal("nil engine accepted")
	}
	valid.MaxInputBytes = -1
	if _, err := prepare(context.Background(), &fakeEngine{}, valid, true); err == nil {
		t.Fatal("negative input limit accepted")
	}
}

type inputReader struct {
	io.Reader
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
