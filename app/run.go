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

type sessionFactory struct {
	env            environment
	store          *sessionStore
	messageHistory *messageHistoryStore
	home           string
}

func (factory sessionFactory) create(ctx context.Context, config resolvedConfig, selected backend.Backend) (*agentSession, error) {
	runtime, err := openRuntime(ctx, selected, factory.home)
	if err != nil {
		return nil, err
	}
	return newStoredAgentSession(config, factory.env, runtime, factory.store, factory.messageHistory, nil)
}

func (factory sessionFactory) open(ctx context.Context, cwd, sessionID string) (*agentSession, resolvedConfig, backend.Backend, error) {
	config, handle, selected, err := resolveStoredSession(ctx, factory.store, factory.env, cwd, sessionID)
	if err != nil {
		return nil, resolvedConfig{}, backend.Backend{}, err
	}
	runtime, err := openRuntime(ctx, selected, factory.home)
	if err != nil {
		_ = handle.Close()
		return nil, resolvedConfig{}, backend.Backend{}, err
	}
	session, err := newStoredAgentSession(config, factory.env, runtime, factory.store, factory.messageHistory, handle)
	if err != nil {
		return nil, resolvedConfig{}, backend.Backend{}, err
	}
	return session, config, selected, nil
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
	}
	home := dependencies.Home
	env.newToolset = buildToolset
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)
	factory := sessionFactory{env: env, store: store, messageHistory: messageHistory, home: home}

	config, handle, selected, err := resolveInitialSession(ctx, options, env, store)
	if err != nil {
		return err
	}
	var session *agentSession
	if handle == nil {
		session, err = factory.create(ctx, config, selected)
	} else {
		runtime, openErr := openRuntime(ctx, selected, home)
		if openErr != nil {
			_ = closeSessionHandle(handle)
			return openErr
		}
		session, err = newStoredAgentSession(config, env, runtime, store, messageHistory, handle)
	}
	if err != nil {
		return err
	}

	runner, runnerCloser, err := newRunner(env.stdin, env.stdout)
	if err != nil {
		return session.finish(err)
	}
	runErr := runSessions(ctx, runner, session, config, selected, factory)
	return errors.Join(runErr, runnerCloser.Close())
}

func resolveInitialSession(
	ctx context.Context,
	arguments Options,
	env environment,
	store *sessionStore,
) (resolvedConfig, *sessionHandle, backend.Backend, error) {
	if !arguments.Resume {
		selected, err := env.backends.Lookup(arguments.Provider)
		if err != nil {
			return resolvedConfig{}, nil, backend.Backend{}, err
		}
		config, err := resolveConfig(arguments, env, selected)
		return config, nil, selected, err
	}

	cwd, err := resolveCWD("", env.getwd)
	if err != nil {
		return resolvedConfig{}, nil, backend.Backend{}, err
	}
	config, handle, selected, err := resolveStoredSession(ctx, store, env, cwd, arguments.SessionID)
	config.noSandbox = arguments.NoSandbox
	return config, handle, selected, err
}

func resolveStoredSession(
	ctx context.Context,
	store *sessionStore,
	env environment,
	lookupCWD string,
	sessionID string,
) (resolvedConfig, *sessionHandle, backend.Backend, error) {
	handle, err := store.Open(ctx, lookupCWD, sessionID)
	if err != nil {
		return resolvedConfig{}, nil, backend.Backend{}, err
	}
	record := handle.Record()
	selected, err := env.backends.Lookup(record.Provider)
	if err != nil {
		_ = handle.Close()
		return resolvedConfig{}, nil, backend.Backend{}, err
	}
	resolved, err := resolveConfig(optionsFromRecord(record), env, selected)
	if err != nil {
		_ = handle.Close()
		return resolvedConfig{}, nil, backend.Backend{}, err
	}
	resolved.warnings = append(resolved.warnings, handle.warnings...)
	return resolved, handle, selected, nil
}

func runSessions(
	ctx context.Context,
	runner sessionRunner,
	session *agentSession,
	config resolvedConfig,
	selected backend.Backend,
	factory sessionFactory,
) error {
	for {
		outcome, runErr := session.run(ctx, runner)
		if runErr != nil {
			return runErr
		}

		switch outcome.Action {
		case terminal.RunExit:
			return nil
		case terminal.RunNewSession:
			var err error
			nextOptions := optionsFromConfig(config)
			nextOptions.ThinkingLevel, nextOptions.FastMode = session.settings.Snapshot()
			config, err = resolveConfig(nextOptions, factory.env, selected)
			if err != nil {
				return err
			}
			session, err = factory.create(ctx, config, selected)
			if err != nil {
				return err
			}
			continue
		case terminal.RunResumeSession:
			var err error
			session, config, selected, err = factory.open(ctx, config.cwd, outcome.SessionID)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("session: unknown terminal run action %d", outcome.Action)
		}
	}
}

func optionsFromRecord(record sessionRecord) Options {
	return Options{
		Models:           record.Models,
		ThinkingLevel:    record.ThinkingLevel,
		FastMode:         record.FastMode,
		WorkingDirectory: record.WorkingDirectory,
	}
}

func optionsFromConfig(config resolvedConfig) Options {
	return Options{
		Models:           config.models,
		ThinkingLevel:    config.thinkingLevel,
		FastMode:         config.fastMode,
		WorkingDirectory: config.cwd,
		NoSandbox:        config.noSandbox,
	}
}

func openRuntime(ctx context.Context, selected backend.Backend, home string) (*backend.Runtime, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	return selected.Open(ctx, home)
}

func closeSessionHandle(handle *sessionHandle) error {
	if handle == nil {
		return nil
	}
	return handle.Close()
}
