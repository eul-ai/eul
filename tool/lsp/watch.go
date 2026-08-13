package lsp

import (
	"context"
	"io/fs"
	"sync"
	"time"

	"go.lsp.dev/protocol"
)

const (
	lspWatchBatchDelay        = 20 * time.Millisecond
	lspWatchNotifyTimeout     = time.Second
	lspWatchSuppressionWindow = time.Second
)

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
