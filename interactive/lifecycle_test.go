package interactive

import (
	"context"
	"errors"
	"io"
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

type lifecycleBackendDriver struct {
	descriptor backend.Descriptor
	runtimes   []*fakeBackendRuntime
	openCalls  int
	beforeOpen func(int)
}

func (driver *lifecycleBackendDriver) Descriptor() backend.Descriptor {
	return driver.descriptor
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
	backendRuntimes := newLifecycleBackendRuntimes(2)
	driver := newLifecycleBackendDriver(backendRuntimes)
	runtime := newLifecycleRuntime(t, cwd, driver)
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
	initialID := initialSession.terminalOptions.Config.SessionID

	oldClosedBeforeOpen := false
	driver.beforeOpen = func(index int) {
		if index != 1 {
			return
		}
		oldClosedBeforeOpen = initialSession.persistence.handle.closed &&
			backendRuntimes[0].closeCalls == 1
	}

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if options.Config.SessionID != initialID || options.Config.Model != "main-model" || options.Config.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.Config.SessionID, options.Config.Model, options.Config.ThinkingLevel)
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
			if options.Config.SessionID == "" || options.Config.SessionID == initialID {
				t.Fatalf("new session ID = %q, initial = %q", options.Config.SessionID, initialID)
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

	if err := runSessions(ctx, runner, initialSession, config, driver, sessionFactory{env: runtime, store: store, home: home}); err != nil {
		t.Fatal(err)
	}
	if !oldClosedBeforeOpen {
		t.Fatal("old persistence and backend were not closed before opening the replacement backend")
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, 2)
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].StateChanges.Notify(terminal.EmptyCheckpoint(), false); err == nil || !strings.Contains(err.Error(), "session is closed") {
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
	backendRuntimes := newLifecycleBackendRuntimes(2)
	driver := newLifecycleBackendDriver(backendRuntimes)
	runtime := newLifecycleRuntime(t, initialCWD, driver)
	store := newSessionStore(home)

	targetTerminal := sessionStoreTestTerminalCheckpoint(t, "resume target prompt")
	target, err := store.Create(
		"test",
		targetCWD,
		modelSet{primary: "resume-main", fast: "resume-fast", balanced: "resume-balanced"},
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
	initialID := initialSession.terminalOptions.Config.SessionID

	oldClosedBeforeOpen := false
	driver.beforeOpen = func(index int) {
		if index != 1 {
			return
		}
		oldClosedBeforeOpen = initialSession.persistence.handle.closed &&
			backendRuntimes[0].closeCalls == 1
	}

	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
			if options.Config.SessionID != initialID || options.Config.Model != "initial-main" || options.Config.ThinkingLevel != agent.ThinkingHigh {
				t.Fatalf("initial options = session %q, model %q, thinking %q", options.Config.SessionID, options.Config.Model, options.Config.ThinkingLevel)
			}
			return terminal.RunOutcome{Action: terminal.RunResumeSession, SessionID: targetID}, nil
		},
		func(_ context.Context, options terminal.Options) (terminal.RunOutcome, error) {
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

	if err := runSessions(ctx, runner, initialSession, config, driver, sessionFactory{env: runtime, store: store, home: home}); err != nil {
		t.Fatal(err)
	}
	if !oldClosedBeforeOpen {
		t.Fatal("old persistence and backend were not closed before opening the resumed backend")
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, 2)
	if len(runner.options) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.options))
	}
	if err := runner.options[1].StateChanges.Notify(targetTerminal, false); err == nil || !strings.Contains(err.Error(), "session is closed") {
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
	runtime := newLifecycleRuntime(t, cwd, driver)
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
	runner := &scriptedSessionRunner{steps: []func(context.Context, terminal.Options) (terminal.RunOutcome, error){
		func(context.Context, terminal.Options) (terminal.RunOutcome, error) {
			return terminal.RunOutcome{Action: terminal.RunExit}, nil
		},
	}}

	if err := runSessions(ctx, runner, initialSession, config, driver, sessionFactory{env: runtime, store: store, home: home}); err != nil {
		t.Fatal(err)
	}
	if driver.openCalls != 1 {
		t.Fatalf("backend open calls = %d, want 1", driver.openCalls)
	}
	assertLifecycleCleanup(t, driver, backendRuntimes, 1)
}

func TestRunPreservesSessionAndRunnerCleanupFailures(t *testing.T) {
	runFailure := errors.New("run failed")
	runnerCleanupFailure := errors.New("runner cleanup failed")
	runner := &runFailureRunner{runErr: runFailure, closeErr: runnerCleanupFailure}

	cwd := t.TempDir()
	home := t.TempDir()
	backendRuntimes := newLifecycleBackendRuntimes(1)
	driver := newLifecycleBackendDriver(backendRuntimes)
	backends, err := backend.NewRegistry("test", driver)
	if err != nil {
		t.Fatal(err)
	}

	err = run(context.Background(), Options{Provider: "test", WorkingDirectory: cwd}, Dependencies{
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
	if backendRuntimes[0].closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backendRuntimes[0].closeCalls)
	}
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
		descriptor: backend.Descriptor{ID: "test", Name: "Test Provider", DefaultModels: backend.ModelDefaults{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"}},
		runtimes:   runtimes,
	}
}

func newLifecycleRuntime(t *testing.T, cwd string, driver backend.Driver) environment {
	t.Helper()
	backends, err := backend.NewRegistry("test", driver)
	if err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	return environment{
		stdin:       strings.NewReader(""),
		stdout:      io.Discard,
		getwd:       func() (string, error) { return cwd, nil },
		userHomeDir: func() (string, error) { return userHome, nil },
		backends:    backends,
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

func assertLifecycleCleanup(t *testing.T, driver *lifecycleBackendDriver, runtimes []*fakeBackendRuntime, want int) {
	t.Helper()
	if driver.openCalls != want {
		t.Fatalf("backend open calls = %d, want %d", driver.openCalls, want)
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
