package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
)

type runnerFactory func(io.Reader, io.Writer) (sessionRunner, io.Closer, error)

type launcher struct {
	env       environment
	home      string
	store     *sessionStore
	history   *messageHistoryStore
	noSandbox bool
	runtimes  map[string]*backend.Runtime
}

func newLauncher(home string, env environment, noSandbox bool) *launcher {
	return &launcher{
		env:       env,
		home:      home,
		store:     newSessionStore(home),
		history:   newMessageHistoryStore(home),
		noSandbox: noSandbox,
		runtimes:  make(map[string]*backend.Runtime),
	}
}

func Run(ctx context.Context, options Options, dependencies Dependencies) error {
	return run(ctx, options, dependencies, func(input io.Reader, output io.Writer) (sessionRunner, io.Closer, error) {
		runner, err := terminal.NewRunner(input, output)
		return runner, runner, err
	})
}

func run(ctx context.Context, options Options, dependencies Dependencies, newRunner runnerFactory) error {
	env := environment{
		stdin:       dependencies.Input,
		stdout:      dependencies.Output,
		getwd:       dependencies.Getwd,
		userHomeDir: dependencies.UserHomeDir,
		interrupts:  dependencies.Interrupts,
		backends:    dependencies.Backends,
		newToolset:  buildToolset,
	}
	launcher := newLauncher(dependencies.Home, env, options.NoSandbox)
	session, err := launcher.launch(ctx, options)
	if err != nil {
		return err
	}

	runner, runnerCloser, err := newRunner(env.stdin, env.stdout)
	if err != nil {
		return session.finish(err)
	}
	runErr := runSessions(ctx, runner, session, launcher)
	return errors.Join(runErr, runnerCloser.Close())
}

func runSessions(ctx context.Context, runner sessionRunner, session *agentSession, launcher *launcher) error {
	for {
		outcome, err := session.run(ctx, runner)
		if err != nil {
			return err
		}

		switch outcome.Action {
		case terminal.RunExit:
			return nil
		case terminal.RunNewSession:
			session, err = launcher.start(ctx, session.settings(), "")
		case terminal.RunResumeSession:
			session, err = launcher.resume(ctx, session.config.cwd, outcome.SessionID, "")
		default:
			return fmt.Errorf("session: unknown terminal run action %d", outcome.Action)
		}
		if err != nil {
			return err
		}
	}
}

func (launcher *launcher) launch(ctx context.Context, options Options) (*agentSession, error) {
	if options.Resume {
		return launcher.resume(ctx, "", options.SessionID, options.Prompt)
	}
	return launcher.start(ctx, sessionSettings{
		provider:      options.Provider,
		models:        options.Models,
		thinkingLevel: options.ThinkingLevel,
		fastMode:      options.FastMode,
		cwd:           options.WorkingDirectory,
	}, options.Prompt)
}

func (launcher *launcher) start(ctx context.Context, settings sessionSettings, prompt string) (*agentSession, error) {
	return launcher.open(ctx, settings, nil, prompt)
}

func (launcher *launcher) resume(ctx context.Context, cwd, id, prompt string) (*agentSession, error) {
	cwd, err := resolveCWD(cwd, launcher.env.getwd)
	if err != nil {
		return nil, err
	}
	handle, err := launcher.store.Open(ctx, cwd, id)
	if err != nil {
		return nil, err
	}

	session, err := launcher.open(ctx, handle.Record().settings(), handle, prompt)
	if err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	return session, nil
}

func (launcher *launcher) open(ctx context.Context, settings sessionSettings, handle *sessionHandle, prompt string) (*agentSession, error) {
	selected, err := launcher.env.backends.Lookup(settings.provider)
	if err != nil {
		return nil, err
	}
	config, err := resolveConfig(settings, selected, launcher.env)
	if err != nil {
		return nil, err
	}
	if handle != nil {
		config.warnings = append(config.warnings, handle.warnings...)
	}

	runtime, err := launcher.runtime(ctx, selected)
	if err != nil {
		return nil, err
	}
	return newStoredAgentSession(launcher, config, runtime, handle, prompt)
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
