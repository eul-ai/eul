package lsp

import (
	"github.com/fsnotify/fsnotify"
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
