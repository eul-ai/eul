package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
	"github.com/eul-ai/eul/tool/subagent"
)

type scriptedSessionRunner struct {
	steps   []func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error)
	options []terminal.Options
}

func (runner *scriptedSessionRunner) Run(ctx context.Context, engine terminal.Engine, options terminal.Options) (terminal.RunOutcome, error) {
	runner.options = append(runner.options, options)
	index := len(runner.options) - 1
	if index >= len(runner.steps) {
		return terminal.RunOutcome{}, errors.New("unexpected session run")
	}
	return runner.steps[index](ctx, engine, options)
}

type lifecycleBackendDriver struct {
	descriptor backend.Descriptor
	defaults   backend.ModelDefaults
	runtimes   []*fakeBackendRuntime
	openCalls  int
	beforeOpen func(int)
}

func (driver *lifecycleBackendDriver) Descriptor() backend.Descriptor {
	return driver.descriptor
}

func (driver *lifecycleBackendDriver) ModelDefaults() backend.ModelDefaults {
	return driver.defaults
}

func (driver *lifecycleBackendDriver) Open(backend.Options) (backend.Runtime, error) {
	index := driver.openCalls
	if driver.beforeOpen != nil {
		driver.beforeOpen(index)
	}
	if index >= len(driver.runtimes) {
		return nil, errors.New("unexpected backend open")
	}
	driver.openCalls++
	return driver.runtimes[index], nil
}

type lifecycleCloser struct {
	err   error
	calls int
}

func (closer *lifecycleCloser) Close() error {
	closer.calls++
	return closer.err
}

type lifecycleToolState struct {
	closeErrors []error
	closers     []*lifecycleCloser
}

func (state *lifecycleToolState) newToolset(_ string, _ toolAccess, _ bool, _ tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
	var closeErr error
	if len(state.closers) < len(state.closeErrors) {
		closeErr = state.closeErrors[len(state.closers)]
	}
	closer := &lifecycleCloser{err: closeErr}
	state.closers = append(state.closers, closer)
	return tool.NewRegistry(additional, closer)
}

func TestRunSessionsStartsNewSessionAfterClosingOldSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	home := t.TempDir()
	backendRuntimes := newLifecycleBackendRuntimes(2)
	driver := newLifecycleBackendDriver(backendRuntimes)
	toolState := &lifecycleToolState{}
	runtime := newLifecycleRuntime(t, cwd, driver, toolState)
	store := newSessionStore(home)
	config := resolveLifecycleConfig(t, runtime, "main-model", "fast-model", "balanced-model", agent.ThinkingHigh, cwd)

	backendRuntime, err := openBackendRuntime(ctx, driver, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialID := initialSession.terminalOptions.SessionID

	oldClosedBeforeOpen := false
	driver.beforeOpen = func(index int) {
		if index != 1 {
			return
		}
		oldClosedBeforeOpen = len(toolState.closers) == 1 &&
			toolState.closers[0].calls == 1 &&
			initialSession.persistence.closed &&
			backendRuntimes[0].closeCalls == 1
	}

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, _ terminal.Engine, options terminal.Options) (terminal.RunOutcome, error) {
			if options.SessionID != initialID || options.Model != "main-model" || options.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.SessionID, options.Model, options.ThinkingLevel)
			}
			if err := options.SetThinkingLevel(agent.ThinkingLow); err != nil {
				t.Fatal(err)
			}
			if err := options.SetFastMode(true); err != nil {
				t.Fatal(err)
			}
			return terminal.RunOutcome{Action: terminal.RunNewSession}, nil
		},
		func(_ context.Context, _ terminal.Engine, options terminal.Options) (terminal.RunOutcome, error) {
			if options.SessionID == "" || options.SessionID == initialID {
				t.Fatalf("new session ID = %q, initial = %q", options.SessionID, initialID)
			}
			if options.Model != "main-model" || options.ThinkingLevel != agent.ThinkingLow || !options.FastMode {
				t.Fatalf("new options = model %q, thinking %q, fast %v", options.Model, options.ThinkingLevel, options.FastMode)
			}
			if err := options.SaveCheckpoint(sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "new session prompt"), false); err != nil {
				t.Fatal(err)
			}
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, config, driver, runtime, store, home); err != nil {
		t.Fatal(err)
	}
	if !oldClosedBeforeOpen {
		t.Fatal("old tools, persistence, and backend were not closed before opening the replacement backend")
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, toolState, 2)
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].SaveCheckpoint(sessionStoreTestAgentCheckpoint(t), terminal.EmptyCheckpoint(), false); err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("save after new session cleanup = %v", err)
	}
	newRecord, err := store.Open(ctx, cwd, runner.options[1].SessionID)
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
	backendRuntimes := newLifecycleBackendRuntimes(2)
	driver := newLifecycleBackendDriver(backendRuntimes)
	toolState := &lifecycleToolState{}
	runtime := newLifecycleRuntime(t, initialCWD, driver, toolState)
	store := newSessionStore(home)

	targetTerminal := sessionStoreTestTerminalCheckpoint(t, "resume target prompt")
	target, err := store.Create(
		"test",
		targetCWD,
		modelSelection{main: "resume-main", fast: "resume-fast", balanced: "resume-balanced"},
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
	backendRuntime, err := openBackendRuntime(ctx, driver, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialID := initialSession.terminalOptions.SessionID

	oldClosedBeforeOpen := false
	driver.beforeOpen = func(index int) {
		if index != 1 {
			return
		}
		oldClosedBeforeOpen = len(toolState.closers) == 1 &&
			toolState.closers[0].calls == 1 &&
			initialSession.persistence.closed &&
			backendRuntimes[0].closeCalls == 1
	}

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, _ terminal.Engine, options terminal.Options) (terminal.RunOutcome, error) {
			if options.SessionID != initialID || options.Model != "initial-main" || options.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.SessionID, options.Model, options.ThinkingLevel)
			}
			return terminal.RunOutcome{Action: terminal.RunResumeSession, SessionID: targetID}, nil
		},
		func(_ context.Context, _ terminal.Engine, options terminal.Options) (terminal.RunOutcome, error) {
			if options.SessionID != targetID {
				t.Fatalf("resumed session ID = %q, want %q", options.SessionID, targetID)
			}
			if options.Model != "resume-main" || options.ThinkingLevel != agent.ThinkingMedium || !options.FastMode || options.WorkingDirectory != targetCWD {
				t.Fatalf("resumed options = model %q, thinking %q, fast %v, cwd %q", options.Model, options.ThinkingLevel, options.FastMode, options.WorkingDirectory)
			}
			if options.InitialCheckpoint == nil || options.InitialCheckpoint.Description() != "resume target prompt" {
				t.Fatalf("resumed checkpoint = %#v", options.InitialCheckpoint)
			}
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, config, driver, runtime, store, home); err != nil {
		t.Fatal(err)
	}
	if !oldClosedBeforeOpen {
		t.Fatal("old tools, persistence, and backend were not closed before opening the resumed backend")
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, toolState, 2)
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].SaveCheckpoint(sessionStoreTestAgentCheckpoint(t), targetTerminal, false); err == nil || !strings.Contains(err.Error(), "session is closed") {
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
	backendRuntimes := newLifecycleBackendRuntimes(1)
	driver := newLifecycleBackendDriver(backendRuntimes)
	toolState := &lifecycleToolState{}
	runtime := newLifecycleRuntime(t, cwd, driver, toolState)
	store := newSessionStore(home)
	config := resolveLifecycleConfig(t, runtime, "main-model", "fast-model", "balanced-model", agent.ThinkingHigh, cwd)

	backendRuntime, err := openBackendRuntime(ctx, driver, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error){
		func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error) {
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, config, driver, runtime, store, home); err != nil {
		t.Fatal(err)
	}
	if driver.openCalls != 1 {
		t.Fatalf("backend open calls = %d, want 1", driver.openCalls)
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, toolState, 1)
}

func TestRunSessionsDoesNotHideCleanupFailure(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	home := t.TempDir()
	backendRuntimes := newLifecycleBackendRuntimes(1)
	driver := newLifecycleBackendDriver(backendRuntimes)
	cleanupErr := errors.New("tool cleanup failed")
	toolState := &lifecycleToolState{closeErrors: []error{cleanupErr}}
	runtime := newLifecycleRuntime(t, cwd, driver, toolState)
	store := newSessionStore(home)
	config := resolveLifecycleConfig(t, runtime, "main-model", "fast-model", "balanced-model", agent.ThinkingHigh, cwd)

	backendRuntime, err := openBackendRuntime(ctx, driver, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialSession, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error){
		func(context.Context, terminal.Engine, terminal.Options) (terminal.RunOutcome, error) {
			return terminal.RunOutcome{Action: terminal.RunNewSession}, nil
		},
	}}

	runErr := runSessions(ctx, runner, initialSession, config, driver, runtime, store, home)
	if runErr == nil || !strings.Contains(runErr.Error(), cleanupErr.Error()) {
		t.Fatalf("run error = %v", runErr)
	}
	if driver.openCalls != 1 {
		t.Fatalf("backend open calls = %d, want 1", driver.openCalls)
	}
	if !initialSession.persistence.closed {
		t.Fatal("initial persistence was not closed")
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, toolState, 1)
}

func newLifecycleBackendRuntimes(count int) []*fakeBackendRuntime {
	runtimes := make([]*fakeBackendRuntime, count)
	for index := range runtimes {
		runtimes[index] = &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		}}
	}
	return runtimes
}

func newLifecycleBackendDriver(runtimes []*fakeBackendRuntime) *lifecycleBackendDriver {
	return &lifecycleBackendDriver{
		descriptor: backend.Descriptor{ID: "test", Name: "Test Provider"},
		defaults: backend.ModelDefaults{
			Main:     "default-main",
			Fast:     "default-fast",
			Balanced: "default-balanced",
		},
		runtimes: runtimes,
	}
}

func newLifecycleRuntime(t *testing.T, cwd string, driver backend.Driver, tools *lifecycleToolState) runtime {
	t.Helper()
	backends, err := backend.NewRegistry("test", driver)
	if err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	return runtime{
		stdin:       strings.NewReader(""),
		stdout:      io.Discard,
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: func() (string, error) { return userHome, nil },
		backends:    backends,
		newToolset:  tools.newToolset,
	}
}

func resolveLifecycleConfig(t *testing.T, runtime runtime, main, fast, balanced string, thinking agent.ThinkingLevel, cwd string) resolvedConfig {
	t.Helper()
	config, err := resolveTestConfig(Options{
		Model:            main,
		ModelSet:         true,
		FastModel:        fast,
		FastModelSet:     true,
		BalancedModel:    balanced,
		BalancedModelSet: true,
		ThinkingLevel:    thinking,
		WorkingDirectory: cwd,
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func assertLifecycleCleanup(t *testing.T, driver *lifecycleBackendDriver, runtimes []*fakeBackendRuntime, tools *lifecycleToolState, want int) {
	t.Helper()
	if driver.openCalls != want {
		t.Fatalf("backend open calls = %d, want %d", driver.openCalls, want)
	}
	if len(tools.closers) != want {
		t.Fatalf("tool registries = %d, want %d", len(tools.closers), want)
	}
	for index, closer := range tools.closers {
		if closer.calls != 1 {
			t.Errorf("tool registry %d close calls = %d, want 1", index, closer.calls)
		}
	}
	for index, runtime := range runtimes {
		if runtime.closeCalls != 1 {
			t.Errorf("backend runtime %d close calls = %d, want 1", index, runtime.closeCalls)
		}
	}
}

var (
	_ sessionRunner = (*terminal.Runner)(nil)
	_ sessionRunner = (*scriptedSessionRunner)(nil)
)
