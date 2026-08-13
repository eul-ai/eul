package app

import (
	"context"

	"github.com/eul-ai/eul/backend"
)

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
