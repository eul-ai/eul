package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal"
)

type launcher struct {
	backends   backend.Registry
	interrupts <-chan os.Signal
	home       string
	store      *session.Store
	history    *session.History
	noSandbox  bool
	runtimes   map[string]*backend.Runtime
}

func newLauncher(home string, backends backend.Registry, noSandbox bool, interrupts <-chan os.Signal) *launcher {
	return &launcher{
		backends:   backends,
		interrupts: interrupts,
		home:       home,
		store:      session.NewStore(home),
		history:    session.NewHistory(home),
		noSandbox:  noSandbox,
		runtimes:   make(map[string]*backend.Runtime),
	}
}

func Run(ctx context.Context, options Options, dependencies Dependencies) error {
	launcher := newLauncher(dependencies.Home, dependencies.Backends, options.NoSandbox, dependencies.Interrupts)
	conversation, err := launcher.launch(ctx, options)
	if err != nil {
		return err
	}

	runner, err := terminal.NewRunner(dependencies.Input, dependencies.Output)
	if err != nil {
		return errors.Join(err, conversation.Close())
	}
	runErr := runSessions(ctx, runner.Run, conversation, launcher)
	return errors.Join(runErr, runner.Close())
}

func runSessions(
	ctx context.Context,
	run func(context.Context, terminal.Options) (terminal.RunOutcome, error),
	conversation *session.Session,
	launcher *launcher,
) error {
	for {
		outcome, err := run(ctx, terminal.Options{Session: conversation, Interrupts: launcher.interrupts})
		if err := errors.Join(err, conversation.Close()); err != nil {
			return err
		}

		settings := conversation.Settings()
		switch outcome.Action {
		case terminal.RunExit:
			return nil
		case terminal.RunNewSession:
			conversation, err = launcher.start(ctx, settings, "")
		case terminal.RunResumeSession:
			conversation, err = launcher.resume(ctx, settings.WorkingDirectory, outcome.SessionID, "")
		default:
			return fmt.Errorf("session: unknown terminal run action %d", outcome.Action)
		}
		if err != nil {
			return err
		}
	}
}

func (launcher *launcher) launch(ctx context.Context, options Options) (*session.Session, error) {
	if options.Resume {
		return launcher.resume(ctx, "", options.SessionID, options.Prompt)
	}
	return launcher.start(ctx, session.Settings{
		Provider:         options.Provider,
		Models:           options.Models,
		ThinkingLevel:    options.ThinkingLevel,
		FastMode:         options.FastMode,
		WorkingDirectory: options.WorkingDirectory,
	}, options.Prompt)
}

func (launcher *launcher) start(ctx context.Context, settings session.Settings, prompt string) (*session.Session, error) {
	return launcher.open(ctx, settings, nil, prompt)
}

func (launcher *launcher) resume(ctx context.Context, cwd, id, prompt string) (*session.Session, error) {
	cwd, err := resolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	handle, err := launcher.store.Open(cwd, id)
	if err != nil {
		return nil, err
	}

	resumed, err := launcher.open(ctx, handle.Record().Settings, handle, prompt)
	if err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	return resumed, nil
}

func (launcher *launcher) open(ctx context.Context, settings session.Settings, handle *session.Handle, prompt string) (*session.Session, error) {
	selected, err := launcher.backends.Lookup(settings.Provider)
	if err != nil {
		return nil, err
	}
	config, err := resolveConfig(settings, selected)
	if err != nil {
		return nil, err
	}

	runtime, err := launcher.runtime(ctx, selected)
	if err != nil {
		return nil, err
	}
	if handle == nil {
		handle, err = launcher.store.Create(config.Settings)
		if err != nil {
			return nil, err
		}
	}

	opened, err := launcher.newSession(config, runtime, handle, prompt)
	if err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	return opened, nil
}

func (launcher *launcher) runtime(ctx context.Context, selected backend.Backend) (*backend.Runtime, error) {
	if runtime, ok := launcher.runtimes[selected.ID]; ok {
		return runtime, nil
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	runtime, err := selected.Open(ctx, launcher.home)
	if err != nil {
		return nil, err
	}
	launcher.runtimes[selected.ID] = runtime
	return runtime, nil
}
