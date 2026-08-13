package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type session struct {
	connection           jsonrpc2.Conn
	server               protocol.Server
	client               *protocolClient
	command              *exec.Cmd
	done                 <-chan error
	pullDiagnostics      bool
	renameSupported      bool
	prepareRenameSupport bool
	stopSession          func()
}

type transport struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func startSession(ctx context.Context, cwd string, config serverConfig) (*session, error) {
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	folder := protocol.WorkspaceFolder{URI: uri.File(cwd), Name: filepath.Base(cwd)}
	var server protocol.Server
	watcher, err := newWatchManager(folder, func(ctx context.Context, params *protocol.DidChangeWatchedFilesParams) error {
		return server.DidChangeWatchedFiles(ctx, params)
	})
	if err != nil {
		return nil, fmt.Errorf("start file watcher: %w", err)
	}
	watcherOwned := true
	defer func() {
		if watcherOwned {
			_ = watcher.close()
		}
	}()

	command := exec.Command(config.command, config.arguments...)
	command.Dir = cwd
	command.Stderr = io.Discard

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdin: %w", config.name, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdout: %w", config.name, err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", config.name, err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	client := &protocolClient{
		folder:      folder,
		watcher:     watcher,
		diagnostics: make(map[uri.URI][]protocol.Diagnostic),
		waiters:     make(map[uri.URI][]chan []protocol.Diagnostic),
	}
	var connection jsonrpc2.Conn
	_, connection, server = protocol.NewClient(
		context.Background(),
		client,
		jsonrpc2.NewStream(&transport{reader: stdout, writer: stdin}),
	)
	session := &session{connection: connection, server: server, client: client, command: command, done: done}

	processID := int32(os.Getpid())
	documentChanges := true
	workspaceFolders := true
	dynamicRegistration := false
	watchedFilesSupported := true
	prepareRename := true
	rootURI := folder.URI
	initializeResult, err := server.Initialize(ctx, &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{folder}),
		},
		ProcessID:  &processID,
		ClientInfo: protocol.ClientInfo{Name: "eul"},
		RootURI:    &rootURI,
		Capabilities: protocol.ClientCapabilities{
			General: &protocol.GeneralClientCapabilities{
				PositionEncodings: []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16},
			},
			Workspace: &protocol.WorkspaceClientCapabilities{
				WorkspaceEdit:    &protocol.WorkspaceEditClientCapabilities{DocumentChanges: &documentChanges},
				WorkspaceFolders: &workspaceFolders,
				DidChangeWatchedFiles: &protocol.DidChangeWatchedFilesClientCapabilities{
					DynamicRegistration:    &watchedFilesSupported,
					RelativePatternSupport: &watchedFilesSupported,
				},
			},
			TextDocument: &protocol.TextDocumentClientCapabilities{
				Diagnostic: &protocol.DiagnosticClientCapabilities{DynamicRegistration: &dynamicRegistration},
				Rename:     &protocol.RenameClientCapabilities{PrepareSupport: &prepareRename},
			},
		},
	})
	if err != nil {
		session.abort()
		return nil, err
	}
	session.pullDiagnostics = initializeResult.Capabilities.DiagnosticProvider != nil
	session.renameSupported, session.prepareRenameSupport = renameCapabilities(initializeResult.Capabilities.RenameProvider)
	if err := server.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		session.abort()
		return nil, err
	}

	watcherOwned = false
	return session, nil
}

func renameCapabilities(provider protocol.RenameProvider) (bool, bool) {
	switch provider := provider.(type) {
	case protocol.Boolean:
		return bool(provider), false
	case *protocol.RenameOptions:
		return provider != nil, provider != nil && provider.PrepareProvider != nil && *provider.PrepareProvider
	default:
		return false, false
	}
}

func (s *session) stop() {
	if s.stopSession != nil {
		s.stopSession()
		return
	}
	if s.client.watcher != nil {
		_ = s.client.watcher.close()
	}
	if s.connection.Err() == nil {
		shutdownServer(s.server, lspShutdownTimeout)
	}
	s.abort()
}

func shutdownServer(server protocol.Server, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_ = server.Shutdown(ctx)
	_ = server.Exit(ctx)
}

func (s *session) abort() {
	_ = s.connection.Close()
	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
	<-s.done
}

func (s *session) diagnostics(ctx context.Context, document protocol.TextDocumentIdentifier) (any, error) {
	if s.pullDiagnostics {
		return s.server.Diagnostic(ctx, &protocol.DocumentDiagnosticParams{TextDocument: document})
	}
	return s.client.waitForDiagnostics(ctx, document.URI)
}

func (t *transport) Read(data []byte) (int, error) {
	return t.reader.Read(data)
}

func (t *transport) Write(data []byte) (int, error) {
	return t.writer.Write(data)
}

func (t *transport) Close() error {
	return errors.Join(t.reader.Close(), t.writer.Close())
}
