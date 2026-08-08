package lsp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const (
	lspWatchBatchDelay        = 20 * time.Millisecond
	lspWatchNotifyTimeout     = time.Second
	lspWatchSuppressionWindow = time.Second
)

type lspNativeWatcher interface {
	Add(string) error
	Remove(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyLSPWatcher struct {
	*fsnotify.Watcher
}

func newFSNotifyLSPWatcher() (lspNativeWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyLSPWatcher{Watcher: watcher}, nil
}

func (w *fsnotifyLSPWatcher) Events() <-chan fsnotify.Event { return w.Watcher.Events }
func (w *fsnotifyLSPWatcher) Errors() <-chan error          { return w.Watcher.Errors }

type lspWatchPattern struct {
	glob string
	root string
	kind protocol.WatchKind
}

type lspWatchRegistration struct {
	id       string
	patterns []lspWatchPattern
}

type lspWatchedPathState struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
}

type lspWatchSuppression struct {
	state lspWatchedPathState
	until time.Time
}

type lspWatchCommand func(*lspWatchState)

type lspWatchManager struct {
	folder protocol.WorkspaceFolder
	native lspNativeWatcher
	notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error

	ctx       context.Context
	cancel    context.CancelFunc
	commands  chan lspWatchCommand
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

type lspWatchState struct {
	manager       *lspWatchManager
	registrations map[string][]lspWatchPattern
	watchedDirs   map[string]struct{}
	known         map[string]lspWatchedPathState
	suppressed    map[string]lspWatchSuppression
	pending       map[string]struct{}
	failure       error
}

func newLSPWatchManager(folder protocol.WorkspaceFolder, notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error) (*lspWatchManager, error) {
	native, err := newFSNotifyLSPWatcher()
	if err != nil {
		return nil, err
	}
	return newLSPWatchManagerWithNative(folder, native, notify), nil
}

func newLSPWatchManagerWithNative(folder protocol.WorkspaceFolder, native lspNativeWatcher, notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error) *lspWatchManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &lspWatchManager{
		folder:   folder,
		native:   native,
		notify:   notify,
		ctx:      ctx,
		cancel:   cancel,
		commands: make(chan lspWatchCommand),
		done:     make(chan struct{}),
	}
	go manager.run()
	return manager
}

func (m *lspWatchManager) run() {
	defer close(m.done)

	state := &lspWatchState{
		manager:       m,
		registrations: make(map[string][]lspWatchPattern),
		watchedDirs:   make(map[string]struct{}),
		known:         make(map[string]lspWatchedPathState),
		suppressed:    make(map[string]lspWatchSuppression),
		pending:       make(map[string]struct{}),
	}
	timer := time.NewTimer(lspWatchBatchDelay)
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
				timer.Reset(lspWatchBatchDelay)
				timerChannel = timer.C
			}
		case watchErr, ok := <-m.native.Errors():
			if !ok {
				return
			}
			if errors.Is(watchErr, fsnotify.ErrEventOverflow) {
				if err := state.flushReconciled(); err != nil {
					state.fail(err)
				}
				continue
			}
			state.fail(fmt.Errorf("file watcher: %w", watchErr))
		case <-timerChannel:
			timerChannel = nil
			if err := state.flushPending(); err != nil {
				state.fail(err)
			}
		}
	}
}

func (m *lspWatchManager) register(ctx context.Context, registrations []protocol.Registration) error {
	parsed, err := m.parseRegistrations(registrations)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return nil
	}
	return m.execute(ctx, func(state *lspWatchState) error {
		return state.register(parsed)
	})
}

func (m *lspWatchManager) unregister(ctx context.Context, unregisterations []protocol.Unregistration) error {
	ids := make([]string, 0, len(unregisterations))
	for _, unregistration := range unregisterations {
		if unregistration.Method == protocol.MethodWorkspaceDidChangeWatchedFiles {
			ids = append(ids, unregistration.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return m.execute(ctx, func(state *lspWatchState) error {
		return state.unregister(ids)
	})
}

func (m *lspWatchManager) reportCommitted(ctx context.Context, paths []string) error {
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
	return m.execute(ctx, func(state *lspWatchState) error {
		return state.reportCommitted(resolved)
	})
}

func (m *lspWatchManager) check(ctx context.Context) error {
	return m.execute(ctx, func(state *lspWatchState) error {
		if state.failure == nil {
			state.fail(state.flushPending())
		}
		return state.failure
	})
}

func (m *lspWatchManager) registrationCount(ctx context.Context) (int, error) {
	result := make(chan int, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-m.done:
		return 0, errors.New("language server file watcher is closed")
	case m.commands <- func(state *lspWatchState) { result <- len(state.registrations) }:
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

func (m *lspWatchManager) execute(ctx context.Context, operation func(*lspWatchState) error) error {
	result := make(chan error, 1)
	command := func(state *lspWatchState) { result <- operation(state) }
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return errors.New("language server file watcher is closed")
	case m.commands <- command:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return errors.New("language server file watcher is closed")
	case err := <-result:
		return err
	}
}

func (m *lspWatchManager) close() error {
	m.closeOnce.Do(func() {
		m.cancel()
		m.closeErr = m.native.Close()
		<-m.done
	})
	return m.closeErr
}

func (m *lspWatchManager) parseRegistrations(registrations []protocol.Registration) ([]lspWatchRegistration, error) {
	parsed := make([]lspWatchRegistration, 0, len(registrations))
	ids := make(map[string]struct{})
	for _, registration := range registrations {
		if registration.Method != protocol.MethodWorkspaceDidChangeWatchedFiles {
			continue
		}
		if registration.ID == "" {
			return nil, errors.New("watched-files registration has no id")
		}
		if _, duplicate := ids[registration.ID]; duplicate {
			return nil, fmt.Errorf("duplicate watched-files registration %q", registration.ID)
		}
		ids[registration.ID] = struct{}{}

		data, err := protocol.Marshal(registration.RegisterOptions)
		if err != nil {
			return nil, fmt.Errorf("encode watched-files registration %q: %w", registration.ID, err)
		}
		var options protocol.DidChangeWatchedFilesRegistrationOptions
		if err := protocol.Unmarshal(data, &options); err != nil {
			return nil, fmt.Errorf("decode watched-files registration %q: %w", registration.ID, err)
		}
		patterns := make([]lspWatchPattern, 0, len(options.Watchers))
		for _, watcher := range options.Watchers {
			pattern, err := m.parsePattern(watcher)
			if err != nil {
				return nil, fmt.Errorf("watched-files registration %q: %w", registration.ID, err)
			}
			patterns = append(patterns, pattern)
		}
		parsed = append(parsed, lspWatchRegistration{id: registration.ID, patterns: patterns})
	}
	return parsed, nil
}

func (m *lspWatchManager) parsePattern(watcher protocol.FileSystemWatcher) (lspWatchPattern, error) {
	var base string
	var patternValue protocol.Pattern
	switch pattern := watcher.GlobPattern.(type) {
	case protocol.Pattern:
		base = m.folder.URI.FsPath()
		patternValue = pattern
	case *protocol.RelativePattern:
		var baseURI uri.URI
		switch value := pattern.BaseURI.(type) {
		case protocol.URI:
			baseURI = uri.URI(value)
		case *protocol.WorkspaceFolder:
			baseURI = value.URI
		default:
			return lspWatchPattern{}, fmt.Errorf("unsupported relative pattern base %T", pattern.BaseURI)
		}
		if baseURI.Scheme() != "file" {
			return lspWatchPattern{}, fmt.Errorf("unsupported relative pattern base %q", baseURI)
		}
		base = baseURI.FsPath()
		patternValue = pattern.Pattern
	default:
		return lspWatchPattern{}, fmt.Errorf("unsupported glob pattern %T", watcher.GlobPattern)
	}
	if string(patternValue) == "" {
		return lspWatchPattern{}, errors.New("glob pattern is empty")
	}

	glob := filepath.ToSlash(string(patternValue))
	if !filepath.IsAbs(filepath.FromSlash(glob)) {
		glob = path.Join(filepath.ToSlash(base), glob)
	}
	if !doublestar.ValidatePattern(glob) {
		return lspWatchPattern{}, fmt.Errorf("invalid glob pattern %q", patternValue)
	}
	root, err := lspGlobRoot(glob)
	if err != nil {
		return lspWatchPattern{}, err
	}
	kind := watcher.Kind
	if kind == 0 {
		kind = protocol.WatchKindCreate | protocol.WatchKindChange | protocol.WatchKindDelete
	}
	return lspWatchPattern{glob: glob, root: root, kind: kind}, nil
}

func lspGlobRoot(glob string) (string, error) {
	firstMeta := strings.IndexAny(glob, "*?[{")
	if firstMeta < 0 {
		root := filepath.Dir(filepath.FromSlash(glob))
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("glob pattern %q is not absolute", glob)
		}
		return filepath.Clean(root), nil
	}
	prefix := glob[:firstMeta]
	separator := strings.LastIndex(prefix, "/")
	if separator < 0 {
		return "", fmt.Errorf("glob pattern %q has no absolute root", glob)
	}
	root := filepath.FromSlash(prefix[:separator])
	if root == "" {
		root = string(filepath.Separator)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("glob pattern %q is not absolute", glob)
	}
	return filepath.Clean(root), nil
}

func (state *lspWatchState) register(registrations []lspWatchRegistration) error {
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
	if err := state.reconcile(); err != nil {
		for _, registration := range registrations {
			delete(state.registrations, registration.id)
		}
		_ = state.reconcile()
		state.fail(err)
		return err
	}
	return nil
}

func (state *lspWatchState) unregister(ids []string) error {
	removed := make(map[string][]lspWatchPattern)
	for _, id := range ids {
		if patterns, exists := state.registrations[id]; exists {
			removed[id] = patterns
			delete(state.registrations, id)
		}
	}
	if err := state.reconcile(); err != nil {
		maps.Copy(state.registrations, removed)
		_ = state.reconcile()
		state.fail(err)
		return err
	}
	return nil
}

func (state *lspWatchState) reportCommitted(paths []string) error {
	if state.failure != nil {
		return state.failure
	}

	updates := make(map[string]lspWatchedPathState, len(paths))
	notified := make(map[string]struct{}, len(paths))
	events := make([]protocol.FileEvent, 0, len(paths))
	for _, name := range paths {
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
	if err := state.notify(events); err != nil {
		state.fail(err)
		return err
	}

	until := time.Now().Add(lspWatchSuppressionWindow)
	for name, current := range updates {
		state.known[name] = current
		delete(state.pending, name)
		if _, exists := notified[name]; exists {
			state.suppressed[name] = lspWatchSuppression{state: current, until: until}
		} else {
			delete(state.suppressed, name)
		}
	}
	return nil
}

func (state *lspWatchState) flushPending() error {
	if len(state.pending) == 0 {
		return nil
	}
	pending := state.pending
	state.pending = make(map[string]struct{})

	oldPending := make(map[string]lspWatchedPathState, len(pending))
	newPending := make(map[string]lspWatchedPathState, len(pending))
	for name := range pending {
		oldState, existed := state.known[name]
		if existed {
			oldPending[name] = oldState
		}
		info, err := os.Stat(name)
		switch {
		case err == nil:
			if info.IsDir() || existed && oldState.mode.IsDir() {
				return state.flushReconciledFrom(maps.Clone(state.known), pending)
			}
			newPending[name] = lspPathState(info)
		case errors.Is(err, os.ErrNotExist):
			if existed && oldState.mode.IsDir() {
				return state.flushReconciledFrom(maps.Clone(state.known), pending)
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
	return state.notifyChanges(oldPending, newPending, pending)
}

func (state *lspWatchState) flushReconciled() error {
	oldKnown := maps.Clone(state.known)
	pending := state.pending
	state.pending = make(map[string]struct{})
	return state.flushReconciledFrom(oldKnown, pending)
}

func (state *lspWatchState) flushReconciledFrom(oldKnown map[string]lspWatchedPathState, pending map[string]struct{}) error {
	if err := state.reconcile(); err != nil {
		return err
	}
	return state.notifyChanges(oldKnown, state.known, pending)
}

func (state *lspWatchState) notifyChanges(oldKnown, newKnown map[string]lspWatchedPathState, pending map[string]struct{}) error {
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
	return state.notify(events)
}

func (state *lspWatchState) reconcile() error {
	roots := state.roots()
	directories := make(map[string]struct{})
	known := make(map[string]lspWatchedPathState)
	for _, root := range roots {
		watchRoot, recursive, err := existingLSPWatchRoot(root)
		if err != nil {
			return err
		}
		if !recursive {
			info, err := os.Stat(watchRoot)
			if err != nil {
				return err
			}
			directories[watchRoot] = struct{}{}
			known[watchRoot] = lspPathState(info)
			continue
		}
		err = filepath.WalkDir(watchRoot, func(name string, entry fs.DirEntry, walkErr error) error {
			if err := state.manager.ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			clean := filepath.Clean(name)
			if entry.IsDir() {
				directories[clean] = struct{}{}
			}
			known[clean] = lspPathState(info)
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan watched root %s: %w", filepath.ToSlash(watchRoot), err)
		}
	}

	active := maps.Clone(state.watchedDirs)
	added := make([]string, 0)
	for directory := range directories {
		if _, exists := active[directory]; exists {
			continue
		}
		if err := state.manager.native.Add(directory); err != nil {
			for _, addedDirectory := range added {
				_ = state.manager.native.Remove(addedDirectory)
				delete(active, addedDirectory)
			}
			state.watchedDirs = active
			return fmt.Errorf("watch directory %s: %w", filepath.ToSlash(directory), err)
		}
		active[directory] = struct{}{}
		added = append(added, directory)
	}
	for directory := range active {
		if _, needed := directories[directory]; needed {
			continue
		}
		if err := state.manager.native.Remove(directory); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) && !errors.Is(err, fsnotify.ErrClosed) {
			state.watchedDirs = active
			return fmt.Errorf("stop watching directory %s: %w", filepath.ToSlash(directory), err)
		}
		delete(active, directory)
	}
	state.watchedDirs = directories
	state.known = known
	return nil
}

func (state *lspWatchState) roots() []string {
	rootSet := make(map[string]struct{})
	for _, patterns := range state.registrations {
		for _, pattern := range patterns {
			rootSet[pattern.root] = struct{}{}
		}
	}
	roots := make([]string, 0, len(rootSet))
	for candidate := range rootSet {
		covered := false
		for other := range rootSet {
			if candidate != other && lspPathWithin(candidate, other) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, candidate)
		}
	}
	sort.Strings(roots)
	return roots
}

func (state *lspWatchState) matches(name string, changeType protocol.FileChangeType) bool {
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

func (state *lspWatchState) notify(events []protocol.FileEvent) error {
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].URI != events[right].URI {
			return events[left].URI < events[right].URI
		}
		return events[left].Type < events[right].Type
	})
	ctx, cancel := context.WithTimeout(state.manager.ctx, lspWatchNotifyTimeout)
	defer cancel()
	if err := state.manager.notify(ctx, &protocol.DidChangeWatchedFilesParams{Changes: events}); err != nil {
		return fmt.Errorf("notify language server of watched files: %w", err)
	}
	return nil
}

func (state *lspWatchState) fail(err error) {
	if err != nil && state.failure == nil {
		state.failure = err
	}
}

func existingLSPWatchRoot(root string) (string, bool, error) {
	requested := filepath.Clean(root)
	candidate := requested
	for {
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.IsDir() {
				return "", false, fmt.Errorf("watch root %s is not a directory", filepath.ToSlash(candidate))
			}
			return candidate, candidate == requested, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", false, err
		}
		candidate = parent
	}
}

func lspPathState(info fs.FileInfo) lspWatchedPathState {
	return lspWatchedPathState{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
}

func lspPathWithin(name, root string) bool {
	relative, err := filepath.Rel(root, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
