package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type fakeLSPNativeWatcher struct {
	events chan fsnotify.Event
	errors chan error

	mu       sync.Mutex
	watched  map[string]struct{}
	addCalls int
	addError error
	closed   bool
}

func newFakeLSPNativeWatcher() *fakeLSPNativeWatcher {
	return &fakeLSPNativeWatcher{
		events:  make(chan fsnotify.Event),
		errors:  make(chan error, 4),
		watched: make(map[string]struct{}),
	}
}

func (w *fakeLSPNativeWatcher) Add(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.addError != nil {
		return w.addError
	}
	w.addCalls++
	w.watched[filepath.Clean(name)] = struct{}{}
	return nil
}

func (w *fakeLSPNativeWatcher) Remove(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.watched, filepath.Clean(name))
	return nil
}

func (w *fakeLSPNativeWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.events)
	close(w.errors)
	w.mu.Unlock()
	return nil
}

func (w *fakeLSPNativeWatcher) Events() <-chan fsnotify.Event { return w.events }
func (w *fakeLSPNativeWatcher) Errors() <-chan error          { return w.errors }

func (w *fakeLSPNativeWatcher) addCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addCalls
}

func (w *fakeLSPNativeWatcher) watchedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	paths := make([]string, 0, len(w.watched))
	for name := range w.watched {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	return paths
}

type lspWatchNotifications struct {
	changes   chan []protocol.FileEvent
	notifyErr error
}

func newLSPWatchNotifications() *lspWatchNotifications {
	return &lspWatchNotifications{changes: make(chan []protocol.FileEvent, 16)}
}

func (n *lspWatchNotifications) notify(_ context.Context, params *protocol.DidChangeWatchedFilesParams) error {
	if n.notifyErr != nil {
		return n.notifyErr
	}
	changes := slices.Clone(params.Changes)
	n.changes <- changes
	return nil
}

func TestLSPWatchManagerRegistrationAndRecursiveEvents(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	goPath := filepath.Join(nested, "sample.go")
	textPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(goPath, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(textPath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	native := newFakeLSPNativeWatcher()
	notifications := newLSPWatchNotifications()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		notifications.notify,
	)
	defer manager.close()

	goRegistration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.{go,mod}")},
	}})
	relativeRegistration := lspWatchTestRegistration(t, "text", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{
			GlobPattern: &protocol.RelativePattern{BaseURI: protocol.URI(uri.File(root)), Pattern: "**/*.txt"},
			Kind:        protocol.WatchKindChange,
		},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{goRegistration, relativeRegistration}); err != nil {
		t.Fatal(err)
	}
	if got := native.watchedPaths(); !slices.Contains(got, root) || !slices.Contains(got, nested) {
		t.Fatalf("watched paths = %q", got)
	}

	if err := os.WriteFile(goPath, []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: goPath, Op: fsnotify.Write | fsnotify.Chmod}
	assertLSPWatchEvent(t, notifications.changes, goPath, protocol.FileChangeTypeChanged)

	createdText := filepath.Join(root, "created.txt")
	if err := os.WriteFile(createdText, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: createdText, Op: fsnotify.Create}
	assertNoLSPWatchEvent(t, notifications.changes)

	newDirectory := filepath.Join(root, "new", "deeper")
	if err := os.MkdirAll(newDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	newGoPath := filepath.Join(newDirectory, "new.go")
	if err := os.WriteFile(newGoPath, []byte("package new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: filepath.Join(root, "new"), Op: fsnotify.Create}
	assertLSPWatchEvent(t, notifications.changes, newGoPath, protocol.FileChangeTypeCreated)
	if got := native.watchedPaths(); !slices.Contains(got, newDirectory) {
		t.Fatalf("new directory was not watched: %q", got)
	}

	if err := manager.unregister(context.Background(), []protocol.Unregistration{{ID: "go", Method: protocol.MethodWorkspaceDidChangeWatchedFiles}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goPath, []byte("package ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: goPath, Op: fsnotify.Write}
	assertNoLSPWatchEvent(t, notifications.changes)
	if len(native.watchedPaths()) == 0 {
		t.Fatal("overlapping text registration lost its directory watches")
	}

	if err := manager.unregister(context.Background(), []protocol.Unregistration{{ID: "text", Method: protocol.MethodWorkspaceDidChangeWatchedFiles}}); err != nil {
		t.Fatal(err)
	}
	if got := native.watchedPaths(); len(got) != 0 {
		t.Fatalf("watched paths after unregister = %q", got)
	}
}

func TestLSPWatchManagerNormalizesAndFiltersEvents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := newFakeLSPNativeWatcher()
	notifications := newLSPWatchNotifications()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		notifications.notify,
	)
	defer manager.close()
	registration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{registration}); err != nil {
		t.Fatal(err)
	}
	initialAdds := native.addCount()

	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Chmod}
	assertNoLSPWatchEvent(t, notifications.changes)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Remove | fsnotify.Chmod}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeDeleted)

	if err := os.WriteFile(path, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Create | fsnotify.Write}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeCreated)

	if err := os.WriteFile(path, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Create | fsnotify.Rename}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeChanged)
	if got := native.addCount(); got != initialAdds {
		t.Fatalf("regular file events added %d watches, want %d", got, initialAdds)
	}

	if err := os.WriteFile(path, []byte("overflow changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.errors <- fsnotify.ErrEventOverflow
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeChanged)
}

func TestLSPWatchManagerSuppressesKnownCommittedDuplicates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := newFakeLSPNativeWatcher()
	notifications := newLSPWatchNotifications()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		notifications.notify,
	)
	defer manager.close()
	registration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{registration}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.reportCommitted(context.Background(), []string{path}); err != nil {
		t.Fatal(err)
	}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeChanged)

	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	assertNoLSPWatchEvent(t, notifications.changes)

	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeChanged)

	if err := os.WriteFile(path, []byte("later and different"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	assertLSPWatchEvent(t, notifications.changes, path, protocol.FileChangeTypeChanged)
}

func TestLSPWatchManagerKnownCommitsHonorRegistrations(t *testing.T) {
	for _, test := range []struct {
		name         string
		registration *protocol.Registration
	}{
		{name: "no registration"},
		{
			name: "nonmatching glob",
			registration: lspWatchTestRegistrationPointer(t, "text", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
				{GlobPattern: protocol.Pattern("**/*.txt")},
			}}),
		},
		{
			name: "create only",
			registration: lspWatchTestRegistrationPointer(t, "create", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
				{GlobPattern: protocol.Pattern("**/*.go"), Kind: protocol.WatchKindCreate},
			}}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			filePath := filepath.Join(root, "sample.go")
			if err := os.WriteFile(filePath, []byte("before"), 0o644); err != nil {
				t.Fatal(err)
			}
			native := newFakeLSPNativeWatcher()
			notifications := newLSPWatchNotifications()
			manager := newLSPWatchManagerWithNative(
				protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
				native,
				notifications.notify,
			)
			defer manager.close()
			if test.registration != nil {
				if err := manager.register(context.Background(), []protocol.Registration{*test.registration}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filePath, []byte("after"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := manager.reportCommitted(context.Background(), []string{filePath}); err != nil {
				t.Fatal(err)
			}
			assertNoLSPWatchEvent(t, notifications.changes)
		})
	}
}

func TestLSPWatchManagerKnownCommitAfterNativeEventIsNotDuplicated(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "sample.go")
	if err := os.WriteFile(filePath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := newFakeLSPNativeWatcher()
	notifications := newLSPWatchNotifications()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		notifications.notify,
	)
	defer manager.close()
	registration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{registration}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	native.events <- fsnotify.Event{Name: filePath, Op: fsnotify.Write}
	assertLSPWatchEvent(t, notifications.changes, filePath, protocol.FileChangeTypeChanged)
	if err := manager.reportCommitted(context.Background(), []string{filePath}); err != nil {
		t.Fatal(err)
	}
	assertNoLSPWatchEvent(t, notifications.changes)
}

func TestLSPWatchManagerKnownCommitFailureIsTransactional(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "sample.go")
	if err := os.WriteFile(filePath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := newFakeLSPNativeWatcher()
	notifications := newLSPWatchNotifications()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		notifications.notify,
	)
	defer manager.close()
	registration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{registration}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing.go")
	if err := manager.reportCommitted(context.Background(), []string{filePath, missing}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report error = %v", err)
	}
	assertNoLSPWatchEvent(t, notifications.changes)
	native.events <- fsnotify.Event{Name: filePath, Op: fsnotify.Write}
	assertLSPWatchEvent(t, notifications.changes, filePath, protocol.FileChangeTypeChanged)

	notifyErr := errors.New("notify failed")
	notifications.notifyErr = notifyErr
	if err := os.WriteFile(filePath, []byte("again"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.reportCommitted(context.Background(), []string{filePath}); !errors.Is(err, notifyErr) {
		t.Fatalf("report error = %v", err)
	}
	if err := manager.check(context.Background()); !errors.Is(err, notifyErr) {
		t.Fatalf("watcher error = %v", err)
	}
}

func TestLSPWatchManagerRejectsRegistrationWithoutChangingExistingWatches(t *testing.T) {
	root := t.TempDir()
	native := newFakeLSPNativeWatcher()
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		newLSPWatchNotifications().notify,
	)
	defer manager.close()
	valid := lspWatchTestRegistration(t, "valid", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{valid}); err != nil {
		t.Fatal(err)
	}
	before := native.watchedPaths()

	invalid := lspWatchTestRegistration(t, "invalid", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/[invalid")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{invalid}); err == nil {
		t.Fatal("invalid registration succeeded")
	}
	count, err := manager.registrationCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !slices.Equal(native.watchedPaths(), before) {
		t.Fatalf("registration count = %d, watched paths = %q, want %q", count, native.watchedPaths(), before)
	}

	unrelated := protocol.Registration{ID: "unrelated", Method: protocol.MethodTextDocumentHover}
	if err := manager.register(context.Background(), []protocol.Registration{unrelated}); err != nil {
		t.Fatal(err)
	}
	count, err = manager.registrationCount(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("registration count = %d, error = %v", count, err)
	}
}

func TestLSPWatchManagerSurfacesSetupFailureAndCloses(t *testing.T) {
	root := t.TempDir()
	native := newFakeLSPNativeWatcher()
	addErr := errors.New("watch unavailable")
	native.addError = addErr
	manager := newLSPWatchManagerWithNative(
		protocol.WorkspaceFolder{URI: uri.File(root), Name: filepath.Base(root)},
		native,
		newLSPWatchNotifications().notify,
	)
	registration := lspWatchTestRegistration(t, "go", protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: []protocol.FileSystemWatcher{
		{GlobPattern: protocol.Pattern("**/*.go")},
	}})
	if err := manager.register(context.Background(), []protocol.Registration{registration}); !errors.Is(err, addErr) {
		t.Fatalf("register error = %v", err)
	}
	if err := manager.check(context.Background()); !errors.Is(err, addErr) {
		t.Fatalf("watcher error = %v", err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
}

func lspWatchTestRegistrationPointer(t *testing.T, id string, options protocol.DidChangeWatchedFilesRegistrationOptions) *protocol.Registration {
	registration := lspWatchTestRegistration(t, id, options)
	return &registration
}

func lspWatchTestRegistration(t *testing.T, id string, options protocol.DidChangeWatchedFilesRegistrationOptions) protocol.Registration {
	t.Helper()
	data, err := protocol.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Registration{
		ID:              id,
		Method:          protocol.MethodWorkspaceDidChangeWatchedFiles,
		RegisterOptions: data,
	}
}

func assertLSPWatchEvent(t *testing.T, changes <-chan []protocol.FileEvent, path string, changeType protocol.FileChangeType) {
	t.Helper()
	select {
	case events := <-changes:
		for _, event := range events {
			if event.URI == uri.File(path) && event.Type == changeType {
				return
			}
		}
		t.Fatalf("events = %+v, want %s type %d", events, filepath.ToSlash(path), changeType)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s type %d", filepath.ToSlash(path), changeType)
	}
}

func assertNoLSPWatchEvent(t *testing.T, changes <-chan []protocol.FileEvent) {
	t.Helper()
	select {
	case events := <-changes:
		t.Fatalf("unexpected events: %+v", events)
	case <-time.After(4 * lspWatchBatchDelay):
	}
}
