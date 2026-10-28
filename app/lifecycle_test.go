package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type scriptedSessionRunner struct {
	steps   []func(context.Context, terminal.Options) (terminal.RunOutcome, error)
	options []terminal.Options
}

func (runner *scriptedSessionRunner) Run(ctx context.Context, options terminal.Options) (terminal.RunOutcome, error) {
	runner.options = append(runner.options, options)
	index := len(runner.options) - 1
	if index >= len(runner.steps) {
		return terminal.RunOutcome{}, errors.New("unexpected session run")
	}
	return runner.steps[index](ctx, options)
}

type runFailureRunner struct {
	runErr   error
	closeErr error
}

func (runner *runFailureRunner) Run(context.Context, terminal.Options) (terminal.RunOutcome, error) {
	return terminal.RunOutcome{}, runner.runErr
}

func (runner *runFailureRunner) Close() error {
	return runner.closeErr
}

func TestRunSessionsStartsNewSessionAfterClosingOldSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	opened := 0
	launcher := newLifecycleLauncher(t, cwd, testBackendRuntime(emptyProvider()), &opened)
	initialSession := startTestSession(t, launcher, sessionSettings{
		models:        backend.Models{Main: "main-model", Fast: "fast-model", Balanced: "balanced-model"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           cwd,
	})
	initialID := initialSession.terminalOptions.Config.SessionID

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if options.Config.SessionID != initialID || options.Config.Model != "main-model" || options.Config.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.Config.SessionID, options.Config.Model, options.Config.ThinkingLevel)
			}
			if err := options.MessageHistory.Append("initial session prompt"); err != nil {
				t.Fatal(err)
			}
			if err := options.Controls.SetThinkingLevel(agent.ThinkingLow); err != nil {
				t.Fatal(err)
			}
			if err := options.Controls.SetFastMode(true); err != nil {
				t.Fatal(err)
			}
			return terminal.RunOutcome{Action: terminal.RunNewSession}, nil
		},
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if !initialSession.persistence.handle.closed {
				t.Fatal("old session was not closed before the new session started")
			}
			if options.Config.SessionID == "" || options.Config.SessionID == initialID {
				t.Fatalf("new session ID = %q, initial = %q", options.Config.SessionID, initialID)
			}
			if !slices.Equal(options.MessageHistory.Entries, []string{"initial session prompt"}) {
				t.Fatalf("message history = %q", options.MessageHistory.Entries)
			}
			if options.Config.Model != "main-model" || options.Config.ThinkingLevel != agent.ThinkingLow || !options.Config.FastMode {
				t.Fatalf("new options = model %q, thinking %q, fast %v", options.Config.Model, options.Config.ThinkingLevel, options.Config.FastMode)
			}
			if err := options.StateChanges.Notify(sessionStoreTestTerminalCheckpoint(t, "new session prompt"), false); err != nil {
				t.Fatal(err)
			}
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, launcher); err != nil {
		t.Fatal(err)
	}
	if len(runner.options) != 2 || opened != 1 {
		t.Fatalf("runner calls = %d, backend opens = %d", len(runner.options), opened)
	}
	if err := runner.options[1].StateChanges.Notify(terminal.EmptyCheckpoint(), false); err == nil {
		t.Fatalf("save after new session cleanup = %v", err)
	}
	newRecord, err := launcher.store.Open(ctx, cwd, runner.options[1].Config.SessionID)
	if err != nil {
		t.Fatalf("open new session record: %v", err)
	}
	if newRecord.record.Models.Main != "main-model" || newRecord.record.Models.Fast != "fast-model" || newRecord.record.Models.Balanced != "balanced-model" || newRecord.record.ThinkingLevel != agent.ThinkingLow || !newRecord.record.FastMode {
		t.Fatalf("new session record = %+v", newRecord.record)
	}
	if err := newRecord.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionsResumesStoredSessionAfterClosingOldSession(t *testing.T) {
	ctx := context.Background()
	initialCWD := t.TempDir()
	targetCWD := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	backendRuntime.Catalog = testCatalog("initial-main", "initial-fast", "initial-balanced", "resume-main", "resume-fast", "resume-balanced")
	opened := 0
	launcher := newLifecycleLauncher(t, initialCWD, backendRuntime, &opened)
	targetTerminal := sessionStoreTestTerminalCheckpoint(t, "resume target prompt")
	targetID := createTargetSession(t, launcher.store, targetCWD, targetTerminal)

	initialSession := startTestSession(t, launcher, sessionSettings{
		models:        backend.Models{Main: "initial-main", Fast: "initial-fast", Balanced: "initial-balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           initialCWD,
	})
	initialID := initialSession.terminalOptions.Config.SessionID

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if options.Config.SessionID != initialID || options.Config.Model != "initial-main" || options.Config.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.Config.SessionID, options.Config.Model, options.Config.ThinkingLevel)
			}
			return terminal.RunOutcome{Action: terminal.RunResumeSession, SessionID: targetID}, nil
		},
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if !initialSession.persistence.handle.closed {
				t.Fatal("old session was not closed before the resumed session started")
			}
			if options.Config.SessionID != targetID {
				t.Fatalf("resumed session ID = %q, want %q", options.Config.SessionID, targetID)
			}
			if options.Config.Model != "resume-main" || options.Config.ThinkingLevel != agent.ThinkingMedium || !options.Config.FastMode || options.Config.WorkingDirectory != targetCWD {
				t.Fatalf("resumed options = model %q, thinking %q, fast %v, cwd %q", options.Config.Model, options.Config.ThinkingLevel, options.Config.FastMode, options.Config.WorkingDirectory)
			}
			if options.Config.InitialCheckpoint == nil || options.Config.InitialCheckpoint.Description() != "resume target prompt" {
				t.Fatalf("resumed checkpoint = %#v", options.Config.InitialCheckpoint)
			}
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, launcher); err != nil {
		t.Fatal(err)
	}
	if len(runner.options) != 2 || opened != 1 {
		t.Fatalf("runner calls = %d, backend opens = %d", len(runner.options), opened)
	}
	if err := runner.options[1].StateChanges.Notify(targetTerminal, false); err == nil {
		t.Fatalf("save after resumed session cleanup = %v", err)
	}

	reopened, err := launcher.store.Open(ctx, targetCWD, targetID)
	if err != nil {
		t.Fatalf("reopen resumed record: %v", err)
	}
	if reopened.record.ID != targetID || reopened.record.Models.Main != "resume-main" || reopened.record.Models.Fast != "resume-fast" || reopened.record.Models.Balanced != "resume-balanced" || reopened.record.ThinkingLevel != agent.ThinkingMedium || !reopened.record.FastMode {
		t.Fatalf("resumed record = %+v", reopened.record)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionsExitClosesSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	launcher := testLauncher(t, cwd, testBackendRuntime(emptyProvider()))
	initialSession := startTestSession(t, launcher, sessionSettings{cwd: cwd, thinkingLevel: agent.ThinkingHigh})
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(context.Context, terminal.Options) (terminal.RunOutcome, error) {
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, launcher); err != nil {
		t.Fatal(err)
	}
	if !initialSession.persistence.handle.closed {
		t.Fatal("session was not closed")
	}
}

func TestRunSessionsKeepsNoSandboxAcrossResumeAndNewSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	backendRuntime.Catalog = testCatalog("gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra", "resume-main", "resume-fast", "resume-balanced")
	launcher := testLauncher(t, cwd, backendRuntime)
	launcher.noSandbox = true
	targetID := createTargetSession(t, launcher.store, cwd, sessionStoreTestTerminalCheckpoint(t, "resume target prompt"))
	initialSession := startTestSession(t, launcher, sessionSettings{cwd: cwd, thinkingLevel: agent.ThinkingHigh})

	unsandboxed := func(options terminal.Options) {
		if options.Events.PermissionRequests != nil {
			t.Fatalf("session %q asks for network permissions without a sandbox", options.Config.SessionID)
		}
	}
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			unsandboxed(options)
			return terminal.RunOutcome{Action: terminal.RunResumeSession, SessionID: targetID}, nil
		},
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			unsandboxed(options)
			return terminal.RunOutcome{Action: terminal.RunNewSession}, nil
		},
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			unsandboxed(options)
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}
	if err := runSessions(ctx, runner, initialSession, launcher); err != nil {
		t.Fatal(err)
	}
	if len(runner.options) != 3 {
		t.Fatalf("runner calls = %d, want 3", len(runner.options))
	}
}

func TestLaunchResumeKeepsPrompt(t *testing.T) {
	cwd := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	backendRuntime.Catalog = testCatalog("resume-main", "resume-fast", "resume-balanced")
	launcher := testLauncher(t, cwd, backendRuntime)
	targetID := createTargetSession(t, launcher.store, cwd, sessionStoreTestTerminalCheckpoint(t, "resume target prompt"))

	session, err := launcher.launch(context.Background(), Options{Resume: true, SessionID: targetID, Prompt: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
	if session.terminalOptions.Config.SessionID != targetID || session.terminalOptions.Config.InitialPrompt != "continue" {
		t.Fatalf("resumed session = %q, prompt = %q", session.terminalOptions.Config.SessionID, session.terminalOptions.Config.InitialPrompt)
	}
}

func TestRunPreservesSessionAndRunnerCleanupFailures(t *testing.T) {
	runFailure := errors.New("run failed")
	runnerCleanupFailure := errors.New("runner cleanup failed")
	runner := &runFailureRunner{runErr: runFailure, closeErr: runnerCleanupFailure}

	cwd := t.TempDir()
	home := t.TempDir()
	backends := backend.Registry{testBackend("test", testBackendRuntime(emptyProvider()))}
	err := run(context.Background(), Options{Provider: "test", WorkingDirectory: cwd}, Dependencies{
		Input:       strings.NewReader(""),
		Output:      io.Discard,
		Home:        home,
		Getwd:       func() (string, error) { return cwd, nil },
		UserHomeDir: func() (string, error) { return t.TempDir(), nil },
		Backends:    backends,
	}, func(io.Reader, io.Writer) (sessionRunner, io.Closer, error) {
		return runner, runner, nil
	})
	if !errors.Is(err, runFailure) || !errors.Is(err, runnerCleanupFailure) {
		t.Fatalf("run error = %v, want run and runner cleanup failures", err)
	}
}

func newLifecycleLauncher(t *testing.T, cwd string, runtime *backend.Runtime, opened *int) *launcher {
	t.Helper()
	selected := testBackend("test", runtime)
	selected.Open = func(context.Context, string) (*backend.Runtime, error) {
		*opened++
		return runtime, nil
	}
	return newLauncher(t.TempDir(), testEnvironment(t, cwd, selected), false)
}

func createTargetSession(t *testing.T, store *sessionStore, cwd string, terminalCheckpoint terminal.Checkpoint) string {
	t.Helper()
	target, err := store.Create(
		"test",
		cwd,
		backend.Models{Main: "resume-main", Fast: "resume-fast", Balanced: "resume-balanced"},
		agent.ThinkingMedium,
		sessionStoreTestAgentCheckpoint(t),
		subagent.EmptyCheckpoint(),
		terminalCheckpoint,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	return target.record.ID
}

var (
	_ sessionRunner = (*terminal.Runner)(nil)
	_ sessionRunner = (*scriptedSessionRunner)(nil)
)
