package lsp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func (state *watchState) register(ctx context.Context, registrations []watchRegistration) error {
	if state.failure != nil {
		return state.failure
	}
	for _, registration := range registrations {
		if _, exists := state.registrations[registration.id]; exists {
			return fmt.Errorf("watched-files registration %q already exists", registration.id)
		}
	}

	for _, registration := range registrations {
		state.registrations[registration.id] = registration.patterns
	}
	if err := state.reconcile(ctx); err != nil {
		for _, registration := range registrations {
			delete(state.registrations, registration.id)
		}
		rollbackErr := state.reconcile(state.manager.ctx)
		if !watchContextError(ctx, err) {
			state.fail(err)
		}
		state.fail(rollbackErr)
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (state *watchState) unregister(ctx context.Context, ids []string) error {
	removed := make(map[string][]watchPattern)
	for _, id := range ids {
		if patterns, exists := state.registrations[id]; exists {
			removed[id] = patterns
			delete(state.registrations, id)
		}
	}
	if err := state.reconcile(ctx); err != nil {
		maps.Copy(state.registrations, removed)
		rollbackErr := state.reconcile(state.manager.ctx)
		if !watchContextError(ctx, err) {
			state.fail(err)
		}
		state.fail(rollbackErr)
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (state *watchState) reportCommitted(ctx context.Context, paths []string) error {
	if state.failure != nil {
		return state.failure
	}

	updates := make(map[string]watchedPathState, len(paths))
	notified := make(map[string]struct{}, len(paths))
	events := make([]protocol.FileEvent, 0, len(paths))
	for _, name := range paths {
		if err := state.contextError(ctx); err != nil {
			return err
		}
		info, err := os.Stat(name)
		if err != nil {
			return err
		}
		current := lspPathState(info)
		updates[name] = current
		if previous, exists := state.known[name]; exists && previous == current {
			continue
		}
		if state.matches(name, protocol.FileChangeTypeChanged) {
			events = append(events, protocol.FileEvent{URI: uri.File(name), Type: protocol.FileChangeTypeChanged})
			notified[name] = struct{}{}
		}
	}
	if err := state.notify(ctx, events); err != nil {
		if !watchContextError(ctx, err) {
			state.fail(err)
		}
		return err
	}

	until := time.Now().Add(watchSuppressionWindow)
	for name, current := range updates {
		state.known[name] = current
		delete(state.pending, name)
		if _, exists := notified[name]; exists {
			state.suppressed[name] = watchSuppression{state: current, until: until}
		} else {
			delete(state.suppressed, name)
		}
	}
	return nil
}
