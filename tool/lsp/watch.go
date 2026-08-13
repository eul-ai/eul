package lsp

import (
	"context"
	"io/fs"
	"sync"
	"time"

	"go.lsp.dev/protocol"
)

const (
	watchBatchDelay        = 20 * time.Millisecond
	watchNotifyTimeout     = time.Second
	watchSuppressionWindow = time.Second
)

type watchPattern struct {
	glob string
	root string
	kind protocol.WatchKind
}

type watchRegistration struct {
	id       string
	patterns []watchPattern
}

type watchedPathState struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
}

type watchSuppression struct {
	state watchedPathState
	until time.Time
}

type watchCommand func(*watchState)

type watchManager struct {
	folder protocol.WorkspaceFolder
	native nativeWatcher
	notify func(context.Context, *protocol.DidChangeWatchedFilesParams) error

	ctx       context.Context
	cancel    context.CancelFunc
	commands  chan watchCommand
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

type watchState struct {
	manager       *watchManager
	registrations map[string][]watchPattern
	watchedDirs   map[string]struct{}
	known         map[string]watchedPathState
	suppressed    map[string]watchSuppression
	pending       map[string]struct{}
	failure       error
}
