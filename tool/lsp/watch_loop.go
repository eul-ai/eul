package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.lsp.dev/protocol"
)

func newWatchManager(folder protocol.WorkspaceFolder, notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error) (*watchManager, error) {
	native, err := newFSNotifyWatcher()
	if err != nil {
		return nil, err
	}
	return newWatchManagerWithNative(folder, native, notify), nil
}

func newWatchManagerWithNative(folder protocol.WorkspaceFolder, native nativeWatcher, notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error) *watchManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &watchManager{
		folder:   folder,
		native:   native,
		notify:   notify,
		ctx:      ctx,
		cancel:   cancel,
		commands: make(chan watchCommand),
		done:     make(chan struct{}),
	}
	go manager.run()
	return manager
}

func (m *watchManager) run() {
	defer close(m.done)

	state := &watchState{
		manager:       m,
		registrations: make(map[string][]watchPattern),
		watchedDirs:   make(map[string]struct{}),
		known:         make(map[string]watchedPathState),
		suppressed:    make(map[string]watchSuppression),
		pending:       make(map[string]struct{}),
	}
	timer := time.NewTimer(watchBatchDelay)
	timer.Stop()
	defer timer.Stop()
	var timerChannel <-chan time.Time

	for {
		select {
		case <-m.ctx.Done():
			return
		case command := <-m.commands:
			command(state)
		case event, ok := <-m.native.Events():
			if !ok {
				return
			}
			if event.Op&^fsnotify.Chmod == 0 {
				continue
			}
			name, err := filepath.Abs(event.Name)
			if err != nil {
				state.fail(err)
				continue
			}
			state.pending[filepath.Clean(name)] = struct{}{}
			if timerChannel == nil {
				timer.Reset(watchBatchDelay)
				timerChannel = timer.C
			}
		case watchErr, ok := <-m.native.Errors():
			if !ok {
				return
			}
			if errors.Is(watchErr, fsnotify.ErrEventOverflow) {
				if err := state.flushReconciled(m.ctx); err != nil {
					state.fail(err)
				}
				continue
			}
			state.fail(fmt.Errorf("file watcher: %w", watchErr))
		case <-timerChannel:
			timerChannel = nil
			if err := state.flushPending(m.ctx); err != nil {
				state.fail(err)
			}
		}
	}
}

func (m *watchManager) register(ctx context.Context, registrations []protocol.Registration) error {
	parsed, err := m.parseRegistrations(registrations)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return nil
	}
	return m.execute(ctx, func(ctx context.Context, state *watchState) error {
		return state.register(ctx, parsed)
	})
}

func (m *watchManager) unregister(ctx context.Context, unregisterations []protocol.Unregistration) error {
	ids := make([]string, 0, len(unregisterations))
	for _, unregistration := range unregisterations {
		if unregistration.Method == protocol.MethodWorkspaceDidChangeWatchedFiles {
			ids = append(ids, unregistration.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return m.execute(ctx, func(ctx context.Context, state *watchState) error {
		return state.unregister(ctx, ids)
	})
}

func (m *watchManager) reportCommitted(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	resolved := make([]string, len(paths))
	for index, name := range paths {
		absolute, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		resolved[index] = filepath.Clean(absolute)
	}
	return m.execute(ctx, func(ctx context.Context, state *watchState) error {
		return state.reportCommitted(ctx, resolved)
	})
}

func (m *watchManager) check(ctx context.Context) error {
	return m.execute(ctx, func(ctx context.Context, state *watchState) error {
		if state.failure != nil {
			return state.failure
		}
		if err := state.flushPending(ctx); err != nil {
			if !watchContextError(ctx, err) {
				state.fail(err)
			}
			return err
		}
		return nil
	})
}

func (m *watchManager) registrationCount(ctx context.Context) (int, error) {
	result := make(chan int, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-m.done:
		return 0, errors.New("language server file watcher is closed")
	case m.commands <- func(state *watchState) { result <- len(state.registrations) }:
	}

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-m.done:
		return 0, errors.New("language server file watcher is closed")
	case count := <-result:
		return count, nil
	}
}

func (m *watchManager) execute(ctx context.Context, operation func(context.Context, *watchState) error) error {
	result := make(chan error, 1)
	command := func(state *watchState) { result <- operation(ctx, state) }
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return errors.New("language server file watcher is closed")
	case m.commands <- command:
	}

	return <-result
}

func (m *watchManager) close() error {
	m.closeOnce.Do(func() {
		m.cancel()
		m.closeErr = m.native.Close()
		<-m.done
	})
	return m.closeErr
}
