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
	home := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	selected := testBackend("test", backendRuntime)
	runtime := newLifecycleRuntime(t, cwd, selected)
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)
	config := resolveLifecycleConfig(t, runtime, "main-model", "fast-model", "balanced-model", agent.ThinkingHigh, cwd)

	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
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

	if err := runSessions(ctx, runner, initialSession, config, selected, sessionFactory{env: runtime, store: store, messageHistory: messageHistory, home: home}); err != nil {
		t.Fatal(err)
	}
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].StateChanges.Notify(terminal.EmptyCheckpoint(), false); err == nil {
		t.Fatalf("save after new session cleanup = %v", err)
	}
	newRecord, err := store.Open(ctx, cwd, runner.options[1].Config.SessionID)
	if err != nil {
		t.Fatalf("open new session record: %v", err)
	}
	if newRecord.record.Model != "main-model" || newRecord.record.FastModel != "fast-model" || newRecord.record.BalancedModel != "balanced-model" || newRecord.record.ThinkingLevel != agent.ThinkingLow || !newRecord.record.FastMode {
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
	home := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	backendRuntime.Catalog = testCatalog("initial-main", "initial-fast", "initial-balanced", "resume-main", "resume-fast", "resume-balanced")
	selected := testBackend("test", backendRuntime)
	runtime := newLifecycleRuntime(t, initialCWD, selected)
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)

	targetTerminal := sessionStoreTestTerminalCheckpoint(t, "resume target prompt")
	target, err := store.Create(
		"test",
		targetCWD,
		modelSet{main: "resume-main", fast: "resume-fast", balanced: "resume-balanced"},
		agent.ThinkingMedium,
		sessionStoreTestAgentCheckpoint(t),
		subagent.EmptyCheckpoint(),
		targetTerminal,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	targetID := target.record.ID
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	config := resolveLifecycleConfig(t, runtime, "initial-main", "initial-fast", "initial-balanced", agent.ThinkingHigh, initialCWD)
	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
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

	if err := runSessions(ctx, runner, initialSession, config, selected, sessionFactory{env: runtime, store: store, messageHistory: messageHistory, home: home}); err != nil {
		t.Fatal(err)
	}
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].StateChanges.Notify(targetTerminal, false); err == nil {
		t.Fatalf("save after resumed session cleanup = %v", err)
	}

	reopened, err := store.Open(ctx, targetCWD, targetID)
	if err != nil {
		t.Fatalf("reopen resumed record: %v", err)
	}
	if reopened.record.ID != targetID || reopened.record.Model != "resume-main" || reopened.record.FastModel != "resume-fast" || reopened.record.BalancedModel != "resume-balanced" || reopened.record.ThinkingLevel != agent.ThinkingMedium || !reopened.record.FastMode {
		t.Fatalf("resumed record = %+v", reopened.record)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionsExitClosesSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	home := t.TempDir()
	backendRuntime := testBackendRuntime(emptyProvider())
	selected := testBackend("test", backendRuntime)
	runtime := newLifecycleRuntime(t, cwd, selected)
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)
	config := resolveLifecycleConfig(t, runtime, "main-model", "fast-model", "balanced-model", agent.ThinkingHigh, cwd)

	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(context.Context, terminal.Options) (terminal.RunOutcome, error) {
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, config, selected, sessionFactory{env: runtime, store: store, messageHistory: messageHistory, home: home}); err != nil {
		t.Fatal(err)
	}
	if !initialSession.persistence.handle.closed {
		t.Fatal("session was not closed")
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

func newLifecycleRuntime(t *testing.T, cwd string, selected backend.Backend) environment {
	t.Helper()
	userHome := t.TempDir()
	return environment{
		stdin:       strings.NewReader(""),
		stdout:      io.Discard,
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: func() (string, error) { return userHome, nil },
		backends:    backend.Registry{selected},
	}
}

func resolveLifecycleConfig(t *testing.T, env environment, main, fast, balanced string, thinking agent.ThinkingLevel, cwd string) resolvedConfig {
	t.Helper()
	config, err := resolveTestConfig(Options{
		Model:            stringPointer(main),
		FastModel:        stringPointer(fast),
		BalancedModel:    stringPointer(balanced),
		ThinkingLevel:    thinking,
		WorkingDirectory: cwd,
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

var (
	_ sessionRunner = (*terminal.Runner)(nil)
	_ sessionRunner = (*scriptedSessionRunner)(nil)
)
