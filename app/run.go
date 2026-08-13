package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
)

type runnerFactory func(io.Reader, io.Writer) (sessionRunner, io.Closer, error)

type sessionFactory struct {
	env   environment
	store *sessionStore
	home  string
}

func (factory sessionFactory) create(ctx context.Context, config resolvedConfig, driver backend.Driver) (*agentSession, error) {
	backendRuntime, err := openBackendRuntime(ctx, driver, factory.home, factory.env.interrupts)
	if err != nil {
		return nil, err
	}
	return newStoredAgentSession(config, factory.env, backendRuntime, factory.store, nil)
}

func (factory sessionFactory) open(ctx context.Context, cwd, sessionID string) (*agentSession, resolvedConfig, backend.Driver, error) {
	config, handle, driver, err := resolveStoredSession(ctx, factory.store, factory.env, cwd, sessionID)
	if err != nil {
		return nil, resolvedConfig{}, nil, err
	}
	backendRuntime, err := openBackendRuntime(ctx, driver, factory.home, factory.env.interrupts)
	if err != nil {
		_ = handle.Close()
		return nil, resolvedConfig{}, nil, err
	}
	session, err := newStoredAgentSession(config, factory.env, backendRuntime, factory.store, handle)
	if err != nil {
		return nil, resolvedConfig{}, nil, err
	}
	return session, config, driver, nil
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
	factory := sessionFactory{env: env, store: store, home: home}

	config, handle, driver, err := resolveInitialSession(ctx, options, env, store)
	if err != nil {
		return err
	}
	var session *agentSession
	if handle == nil {
		session, err = factory.create(ctx, config, driver)
	} else {
		backendRuntime, openErr := openBackendRuntime(ctx, driver, home, env.interrupts)
		if openErr != nil {
			_ = closeSessionHandle(handle)
			return openErr
		}
		session, err = newStoredAgentSession(config, env, backendRuntime, store, handle)
	}
	if err != nil {
		return err
	}

	runner, runnerCloser, err := newRunner(env.stdin, env.stdout)
	if err != nil {
		return session.finish(err)
	}
	runErr := runSessions(ctx, runner, session, config, driver, factory)
	return errors.Join(runErr, runnerCloser.Close())
}

func resolveInitialSession(
	ctx context.Context,
	arguments Options,
	env environment,
	store *sessionStore,
) (resolvedConfig, *sessionHandle, backend.Driver, error) {
	if !arguments.Resume {
		driver, err := env.backends.Lookup(arguments.Provider)
		if err != nil {
			return resolvedConfig{}, nil, nil, err
		}
		descriptor := driver.Descriptor()
		config, err := resolveConfig(arguments, env, descriptor)
		return config, nil, driver, err
	}

	cwd, err := resolveCWD("", env.getwd)
	if err != nil {
		return resolvedConfig{}, nil, nil, err
	}
	config, handle, driver, err := resolveStoredSession(ctx, store, env, cwd, arguments.SessionID)
	config.noSandbox = arguments.NoSandbox
	return config, handle, driver, err
}

func resolveStoredSession(
	ctx context.Context,
	store *sessionStore,
	env environment,
	lookupCWD string,
	sessionID string,
) (resolvedConfig, *sessionHandle, backend.Driver, error) {
	handle, err := store.Open(ctx, lookupCWD, sessionID)
	if err != nil {
		return resolvedConfig{}, nil, nil, err
	}
	record := handle.Record()
	driver, err := env.backends.Lookup(record.Provider)
	if err != nil {
		_ = handle.Close()
		return resolvedConfig{}, nil, nil, err
	}
	descriptor := driver.Descriptor()
	resolved, err := resolveConfig(optionsFromRecord(record), env, descriptor)
	if err != nil {
		_ = handle.Close()
		return resolvedConfig{}, nil, nil, err
	}
	resolved.warnings = append(resolved.warnings, handle.warnings...)
	return resolved, handle, driver, nil
}

func runSessions(
	ctx context.Context,
	runner sessionRunner,
	session *agentSession,
	config resolvedConfig,
	driver backend.Driver,
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
			descriptor := driver.Descriptor()
			config, err = resolveConfig(nextOptions, factory.env, descriptor)
			if err != nil {
				return err
			}
			session, err = factory.create(ctx, config, driver)
			if err != nil {
				return err
			}
			continue
		case terminal.RunResumeSession:
			var err error
			session, config, driver, err = factory.open(ctx, config.cwd, outcome.SessionID)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("session: unknown terminal run action %d", outcome.Action)
		}
	}
}

func optionsFromRecord(record sessionRecord) Options {
	models := record.models()
	return Options{
		Model:            &models.main,
		FastModel:        &models.fast,
		BalancedModel:    &models.balanced,
		ThinkingLevel:    record.ThinkingLevel,
		FastMode:         record.FastMode,
		WorkingDirectory: record.WorkingDirectory,
	}
}

func optionsFromConfig(config resolvedConfig) Options {
	return Options{
		Model:            &config.models.main,
		FastModel:        &config.models.fast,
		BalancedModel:    &config.models.balanced,
		ThinkingLevel:    config.thinkingLevel,
		FastMode:         config.fastMode,
		WorkingDirectory: config.cwd,
		NoSandbox:        config.noSandbox,
	}
}

func openBackendRuntime(ctx context.Context, driver backend.Driver, home string, interrupts <-chan os.Signal) (backend.Runtime, error) {
	backendRuntime, err := driver.Open(backend.Options{Home: home})
	if err != nil {
		return nil, err
	}
	checker, ok := backendRuntime.(backend.CredentialChecker)
	if !ok {
		return backendRuntime, nil
	}

	credentialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-credentialCtx.Done():
		case _, ok := <-interrupts:
			if ok {
				cancel()
			}
		}
	}()
	if err := checker.CheckCredentials(credentialCtx); err != nil {
		return nil, errors.Join(fmt.Errorf("authentication required: %w", err), closeBackendRuntime(backendRuntime))
	}
	return backendRuntime, nil
}

func closeSessionHandle(handle *sessionHandle) error {
	if handle == nil {
		return nil
	}
	return handle.Close()
}
