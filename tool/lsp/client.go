package lsp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/eul-ai/eul/tool/textfile"
)

type serverConfig struct {
	name       string
	command    string
	arguments  []string
	languageID string
	extensions []string
}

const (
	lspDocumentVersion      = int32(1)
	lspDocumentCloseTimeout = time.Second
	lspShutdownTimeout      = 5 * time.Second
)

func hasAvailableServer(configs []serverConfig) bool {
	for _, config := range configs {
		if _, err := exec.LookPath(config.command); err == nil {
			return true
		}
	}
	return false
}

type client struct {
	requestMu sync.Mutex
	workspace workspace
	configs   []serverConfig
	sessions  map[string]*session
}

type documentRequest func(context.Context, *session, protocol.TextDocumentIdentifier) (any, error)

func newClient(cwd string, configs []serverConfig) *client {
	return &client{workspace: newWorkspace(cwd), configs: configs, sessions: make(map[string]*session)}
}

func (c *client) documentRequest(ctx context.Context, path string, request documentRequest) (any, error) {
	document, err := textfile.Load(path)
	if err != nil {
		return nil, err
	}
	return c.documentSnapshotRequest(ctx, document, request)
}

func (c *client) documentSnapshotRequest(ctx context.Context, document textfile.Snapshot, request documentRequest) (any, error) {
	config, err := c.serverForPath(document.Path)
	if err != nil {
		return nil, err
	}
	session, err := c.session(ctx, config)
	if err != nil {
		return nil, err
	}
	if session.client.watcher != nil {
		if err := session.client.watcher.check(ctx); err != nil {
			c.invalidateSession(config, session)
			return nil, err
		}
	}

	return c.withOpenDocument(ctx, config, session, document.Path, document.Data, request)
}

func (c *client) withOpenDocument(ctx context.Context, config serverConfig, session *session, path string, content []byte, request documentRequest) (any, error) {
	document := protocol.TextDocumentIdentifier{URI: uri.File(path)}
	session.client.clearDiagnostics(document.URI)
	if err := session.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        document.URI,
			LanguageID: protocol.LanguageKind(config.languageID),
			Version:    lspDocumentVersion,
			Text:       string(content),
		},
	}); err != nil {
		c.invalidateSession(config, session)
		return nil, err
	}

	response, requestErr := request(ctx, session, document)
	closeCtx, cancel := context.WithTimeout(context.Background(), lspDocumentCloseTimeout)
	defer cancel()
	closeErr := session.server.DidClose(closeCtx, &protocol.DidCloseTextDocumentParams{TextDocument: document})
	if closeErr != nil {
		c.invalidateSession(config, session)
	}
	return response, errors.Join(requestErr, closeErr)
}

func (c *client) invalidateSession(config serverConfig, session *session) {
	if c.sessions[config.name] != session {
		return
	}
	delete(c.sessions, config.name)
	session.stop()
}

func (c *client) session(ctx context.Context, config serverConfig) (*session, error) {
	if session := c.sessions[config.name]; session != nil {
		select {
		case <-session.connection.Done():
			delete(c.sessions, config.name)
			session.stop()
		default:
			return session, nil
		}
	}

	session, err := startSession(ctx, c.workspace.cwd, config)
	if err != nil {
		return nil, err
	}
	c.sessions[config.name] = session
	return session, nil
}

func (c *client) stop() {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()

	for _, session := range c.sessions {
		session.stop()
	}
	clear(c.sessions)
}

func (c *client) serverForPath(path string) (serverConfig, error) {
	extension := strings.ToLower(filepath.Ext(path))
	for _, config := range c.configs {
		if slices.Contains(config.extensions, extension) {
			return config, nil
		}
	}
	return serverConfig{}, fmt.Errorf("no language server configured for %s", extension)
}
