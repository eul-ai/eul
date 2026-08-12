package session

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/terminal"
	"github.com/eul-ai/eul/tool"
)

func Run(ctx context.Context, options Options, dependencies Dependencies) error {
	runtime := runtime{
		stdin:       dependencies.Input,
		stdout:      dependencies.Output,
		getwd:       dependencies.Getwd,
		userHomeDir: dependencies.UserHomeDir,
		interrupts:  dependencies.Interrupts,
		backends:    dependencies.Backends,
	}
	home := dependencies.Home
	runtime.newToolset = func(cwd string, access toolAccess, authorizeNetwork tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
		return buildToolsetWithHomeAndNetworkAuthorizer(cwd, home, access, authorizeNetwork, additional...)
	}
	store := newSessionStore(home)

	config, handle, driver, err := resolveInitialSession(ctx, options, runtime, store)
	if err != nil {
		return err
	}
	backendRuntime, err := openBackendRuntime(ctx, driver, home, runtime.interrupts)
	if err != nil {
		_ = closeSessionHandle(handle)
		return err
	}
	session, err := newStoredAgentSession(config, runtime, backendRuntime, store, handle)
	if err != nil {
		return err
	}

	runner, err := terminal.NewRunner(runtime.stdin, runtime.stdout)
	if err != nil {
		return session.finish(err)
	}
	return errors.Join(runSessions(ctx, runner, session, config, driver, runtime, store, home), runner.Close())
}

func resolveInitialSession(
	ctx context.Context,
	arguments Options,
	runtime runtime,
	store *sessionStore,
) (resolvedConfig, *sessionHandle, backend.Driver, error) {
	if !arguments.Resume {
		driver, err := runtime.backends.Lookup(arguments.Provider)
		if err != nil {
			return resolvedConfig{}, nil, nil, err
		}
		config, err := resolveConfig(arguments, runtime, driver.Descriptor(), driver.ModelDefaults())
		return config, nil, driver, err
	}

	cwd, err := resolveCWD("", runtime.getwd)
	if err != nil {
		return resolvedConfig{}, nil, nil, err
	}
	config, handle, driver, err := resolveStoredSession(ctx, store, runtime, cwd, arguments.SessionID)
	config.skipPermissions = arguments.SkipPermissions
	return config, handle, driver, err
}

func resolveStoredSession(
	ctx context.Context,
	store *sessionStore,
	runtime runtime,
	lookupCWD string,
	sessionID string,
) (resolvedConfig, *sessionHandle, backend.Driver, error) {
	handle, err := store.Open(ctx, lookupCWD, sessionID)
	if err != nil {
		return resolvedConfig{}, nil, nil, err
	}
	record := handle.Record()
	driver, err := runtime.backends.Lookup(record.Provider)
	if err != nil {
		_ = handle.Close()
		return resolvedConfig{}, nil, nil, err
	}
	models := record.models()
	resolved, err := resolveConfig(Options{
		Model:            models.main,
		ModelSet:         true,
		FastModel:        models.fast,
		FastModelSet:     models.fast != "",
		BalancedModel:    models.balanced,
		BalancedModelSet: models.balanced != "",
		ThinkingLevel:    record.ThinkingLevel,
		WorkingDirectory: record.WorkingDirectory,
	}, runtime, driver.Descriptor(), driver.ModelDefaults())
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
	runtime runtime,
	store *sessionStore,
	home string,
) error {
	for {
		runErr := session.run(ctx, runner)
		if onlyNewSessionRequest(runErr) {
			var err error
			config, err = resolveConfig(Options{
				Model:            config.models.main,
				ModelSet:         true,
				FastModel:        config.models.fast,
				FastModelSet:     true,
				BalancedModel:    config.models.balanced,
				BalancedModelSet: true,
				ThinkingLevel:    session.thinkingLevel,
				WorkingDirectory: config.cwd,
			}, runtime, driver.Descriptor(), driver.ModelDefaults())
			if err != nil {
				return err
			}
			backendRuntime, err := openBackendRuntime(ctx, driver, home, runtime.interrupts)
			if err != nil {
				return err
			}
			session, err = newStoredAgentSession(config, runtime, backendRuntime, store, nil)
			if err != nil {
				return err
			}
			continue
		}

		request, resume := onlyResumeRequest(runErr)
		if !resume {
			return runErr
		}

		var handle *sessionHandle
		var err error
		config, handle, driver, err = resolveStoredSession(ctx, store, runtime, config.cwd, request.SessionID)
		if err != nil {
			return err
		}
		backendRuntime, err := openBackendRuntime(ctx, driver, home, runtime.interrupts)
		if err != nil {
			_ = handle.Close()
			return err
		}
		session, err = newStoredAgentSession(config, runtime, backendRuntime, store, handle)
		if err != nil {
			return err
		}
	}
}

func onlyNewSessionRequest(err error) bool {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		_, ok := err.(*terminal.NewSessionRequest)
		return ok
	}

	causes := joined.Unwrap()
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if !onlyNewSessionRequest(cause) {
			return false
		}
	}
	return true
}

func onlyResumeRequest(err error) (*terminal.ResumeRequest, bool) {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		request, ok := err.(*terminal.ResumeRequest)
		return request, ok
	}

	causes := joined.Unwrap()
	if len(causes) == 0 {
		return nil, false
	}
	var selected *terminal.ResumeRequest
	for _, cause := range causes {
		request, ok := onlyResumeRequest(cause)
		if !ok || selected != nil && selected.SessionID != request.SessionID {
			return nil, false
		}
		selected = request
	}
	return selected, selected != nil
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
