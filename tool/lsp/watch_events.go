package lsp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func (state *watchState) flushPending(ctx context.Context) (flushErr error) {
	if len(state.pending) == 0 {
		return state.contextError(ctx)
	}
	pending := state.pending
	state.pending = make(map[string]struct{})
	knownBefore := maps.Clone(state.known)
	suppressedBefore := maps.Clone(state.suppressed)
	defer func() {
		if flushErr == nil {
			return
		}
		maps.Copy(state.pending, pending)
		state.known = knownBefore
		state.suppressed = suppressedBefore
	}()

	oldPending := make(map[string]watchedPathState, len(pending))
	newPending := make(map[string]watchedPathState, len(pending))
	for name := range pending {
		if err := state.contextError(ctx); err != nil {
			return err
		}
		oldState, existed := state.known[name]
		if existed {
			oldPending[name] = oldState
		}
		info, err := os.Stat(name)
		switch {
		case err == nil:
			if info.IsDir() || existed && oldState.mode.IsDir() {
				return state.flushReconciledFrom(ctx, maps.Clone(state.known), pending)
			}
			newPending[name] = lspPathState(info)
		case errors.Is(err, os.ErrNotExist):
			if existed && oldState.mode.IsDir() {
				return state.flushReconciledFrom(ctx, maps.Clone(state.known), pending)
			}
		default:
			return err
		}
	}
	for name := range pending {
		if current, exists := newPending[name]; exists {
			state.known[name] = current
		} else {
			delete(state.known, name)
		}
	}
	return state.notifyChanges(ctx, oldPending, newPending, pending)
}

func (state *watchState) flushReconciled(ctx context.Context) (flushErr error) {
	oldKnown := maps.Clone(state.known)
	pending := state.pending
	state.pending = make(map[string]struct{})
	suppressedBefore := maps.Clone(state.suppressed)
	defer func() {
		if flushErr == nil {
			return
		}
		maps.Copy(state.pending, pending)
		state.known = oldKnown
		state.suppressed = suppressedBefore
	}()
	return state.flushReconciledFrom(ctx, oldKnown, pending)
}

func (state *watchState) flushReconciledFrom(ctx context.Context, oldKnown map[string]watchedPathState, pending map[string]struct{}) error {
	if err := state.reconcile(ctx); err != nil {
		rollbackErr := state.reconcile(state.manager.ctx)
		state.fail(rollbackErr)
		return errors.Join(err, rollbackErr)
	}
	return state.notifyChanges(ctx, oldKnown, state.known, pending)
}

func (state *watchState) notifyChanges(ctx context.Context, oldKnown, newKnown map[string]watchedPathState, pending map[string]struct{}) error {
	eventsByPath := make(map[string]protocol.FileChangeType)
	for name, oldState := range oldKnown {
		newState, exists := newKnown[name]
		switch {
		case !exists:
			eventsByPath[name] = protocol.FileChangeTypeDeleted
		case oldState != newState:
			eventsByPath[name] = protocol.FileChangeTypeChanged
		}
	}
	for name := range newKnown {
		if _, existed := oldKnown[name]; !existed {
			eventsByPath[name] = protocol.FileChangeTypeCreated
		}
	}
	for name := range pending {
		_, existed := oldKnown[name]
		newState, exists := newKnown[name]
		switch {
		case existed && exists:
			if suppressed, ok := state.suppressed[name]; ok {
				delete(state.suppressed, name)
				if suppressed.state == newState && time.Now().Before(suppressed.until) {
					delete(eventsByPath, name)
					continue
				}
			}
			eventsByPath[name] = protocol.FileChangeTypeChanged
		case existed:
			delete(state.suppressed, name)
			eventsByPath[name] = protocol.FileChangeTypeDeleted
		case exists:
			delete(state.suppressed, name)
			eventsByPath[name] = protocol.FileChangeTypeCreated
		}
	}

	events := make([]protocol.FileEvent, 0, len(eventsByPath))
	for name, changeType := range eventsByPath {
		if state.matches(name, changeType) {
			events = append(events, protocol.FileEvent{URI: uri.File(name), Type: changeType})
		}
	}
	return state.notify(ctx, events)
}

func (state *watchState) matches(name string, changeType protocol.FileChangeType) bool {
	kind := protocol.WatchKindChange
	switch changeType {
	case protocol.FileChangeTypeCreated:
		kind = protocol.WatchKindCreate
	case protocol.FileChangeTypeDeleted:
		kind = protocol.WatchKindDelete
	}
	slashed := filepath.ToSlash(name)
	for _, patterns := range state.registrations {
		for _, pattern := range patterns {
			if pattern.kind&kind == 0 {
				continue
			}
			if doublestar.MatchUnvalidated(pattern.glob, slashed) {
				return true
			}
		}
	}
	return false
}

func (state *watchState) notify(ctx context.Context, events []protocol.FileEvent) error {
	if len(events) == 0 {
		return state.contextError(ctx)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].URI != events[right].URI {
			return events[left].URI < events[right].URI
		}
		return events[left].Type < events[right].Type
	})
	notifyCtx, cancel := context.WithTimeout(ctx, watchNotifyTimeout)
	stop := context.AfterFunc(state.manager.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if err := state.manager.notify(notifyCtx, &protocol.DidChangeWatchedFilesParams{Changes: events}); err != nil {
		return fmt.Errorf("notify language server of watched files: %w", err)
	}
	return nil
}

func (state *watchState) contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return state.manager.ctx.Err()
}

func watchContextError(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}

func (state *watchState) fail(err error) {
	if err != nil && state.failure == nil {
		state.failure = err
	}
}
