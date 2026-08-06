package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type lspServerConfig struct {
	name       string
	command    string
	arguments  []string
	languageID string
	extensions []string
}

const lspShutdownTimeout = 5 * time.Second

var lspServerConfigs = []lspServerConfig{
	{
		name:       "gopls",
		command:    "gopls",
		languageID: "go",
		extensions: []string{".go"},
	},
}

type lspClient struct {
	workspace workspace
	sessions  map[string]*lspSession
}

type lspSession struct {
	connection      jsonrpc2.Conn
	server          protocol.Server
	client          *lspProtocolClient
	command         *exec.Cmd
	done            <-chan error
	pullDiagnostics bool
}

type lspTransport struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

type lspProtocolClient struct {
	protocol.UnimplementedClient
	folder protocol.WorkspaceFolder

	mu          sync.Mutex
	diagnostics map[uri.URI][]protocol.Diagnostic
	waiters     map[uri.URI][]chan []protocol.Diagnostic
}

type lspDocumentRequest func(context.Context, *lspSession, protocol.TextDocumentIdentifier) (any, error)

func newLSPClient(cwd string) *lspClient {
	return &lspClient{workspace: newWorkspace(cwd), sessions: make(map[string]*lspSession)}
}

func (c *lspClient) documentRequest(ctx context.Context, path string, request lspDocumentRequest) (any, error) {
	config, err := lspServerForPath(path)
	if err != nil {
		return nil, err
	}
	content, err := readLSPDocument(path)
	if err != nil {
		return nil, err
	}
	session, err := c.session(ctx, config)
	if err != nil {
		return nil, err
	}

	document := protocol.TextDocumentIdentifier{URI: uri.File(path)}
	session.client.clearDiagnostics(document.URI)
	if err := session.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        document.URI,
			LanguageID: protocol.LanguageKind(config.languageID),
			Version:    1,
			Text:       string(content),
		},
	}); err != nil {
		return nil, err
	}
	defer session.server.DidClose(ctx, &protocol.DidCloseTextDocumentParams{TextDocument: document})

	return request(ctx, session, document)
}

func (c *lspClient) session(ctx context.Context, config lspServerConfig) (*lspSession, error) {
	if session := c.sessions[config.name]; session != nil {
		select {
		case <-session.connection.Done():
			delete(c.sessions, config.name)
			session.stop()
		default:
			return session, nil
		}
	}

	session, err := startLSPSession(ctx, c.workspace.cwd, config)
	if err != nil {
		return nil, err
	}
	c.sessions[config.name] = session
	return session, nil
}

func startLSPSession(ctx context.Context, cwd string, config lspServerConfig) (*lspSession, error) {
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

	folder := protocol.WorkspaceFolder{URI: uri.File(cwd), Name: filepath.Base(cwd)}
	client := &lspProtocolClient{
		folder:      folder,
		diagnostics: make(map[uri.URI][]protocol.Diagnostic),
		waiters:     make(map[uri.URI][]chan []protocol.Diagnostic),
	}
	_, connection, server := protocol.NewClient(
		context.Background(),
		client,
		jsonrpc2.NewStream(&lspTransport{reader: stdout, writer: stdin}),
	)
	session := &lspSession{connection: connection, server: server, client: client, command: command, done: done}

	processID := int32(os.Getpid())
	documentChanges := true
	workspaceFolders := true
	dynamicRegistration := false
	prepareRename := true
	rootURI := folder.URI
	initializeResult, err := server.Initialize(ctx, &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{
			WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{folder}),
		},
		ProcessID:  &processID,
		ClientInfo: protocol.ClientInfo{Name: "yaah"},
		RootURI:    &rootURI,
		Capabilities: protocol.ClientCapabilities{
			General: &protocol.GeneralClientCapabilities{
				PositionEncodings: []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16},
			},
			Workspace: &protocol.WorkspaceClientCapabilities{
				WorkspaceEdit:    &protocol.WorkspaceEditClientCapabilities{DocumentChanges: &documentChanges},
				WorkspaceFolders: &workspaceFolders,
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
	if err := server.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		session.abort()
		return nil, err
	}

	return session, nil
}

func (c *lspClient) stop() {
	for _, session := range c.sessions {
		session.stop()
	}
	clear(c.sessions)
}

func (s *lspSession) stop() {
	if s.connection.Err() == nil {
		shutdownLSPServer(s.server, lspShutdownTimeout)
	}
	s.abort()
}

func shutdownLSPServer(server protocol.Server, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_ = server.Shutdown(ctx)
	_ = server.Exit(ctx)
}

func (s *lspSession) abort() {
	_ = s.connection.Close()
	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
	<-s.done
}

func (s *lspSession) diagnostics(ctx context.Context, document protocol.TextDocumentIdentifier) (any, error) {
	if s.pullDiagnostics {
		return s.server.Diagnostic(ctx, &protocol.DocumentDiagnosticParams{TextDocument: document})
	}
	return s.client.waitForDiagnostics(ctx, document.URI)
}

func (c *lspProtocolClient) PublishDiagnostics(_ context.Context, params *protocol.PublishDiagnosticsParams) error {
	diagnostics := slices.Clone(params.Diagnostics)

	c.mu.Lock()
	c.diagnostics[params.URI] = diagnostics
	waiters := c.waiters[params.URI]
	delete(c.waiters, params.URI)
	c.mu.Unlock()

	for _, waiter := range waiters {
		waiter <- diagnostics
	}
	return nil
}

func (c *lspProtocolClient) clearDiagnostics(documentURI uri.URI) {
	c.mu.Lock()
	delete(c.diagnostics, documentURI)
	c.mu.Unlock()
}

func (c *lspProtocolClient) waitForDiagnostics(ctx context.Context, documentURI uri.URI) (*protocol.FullDocumentDiagnosticReport, error) {
	c.mu.Lock()
	if diagnostics, exists := c.diagnostics[documentURI]; exists {
		c.mu.Unlock()
		return &protocol.FullDocumentDiagnosticReport{Kind: string(protocol.DocumentDiagnosticReportKindFull), Items: diagnostics}, nil
	}
	waiter := make(chan []protocol.Diagnostic, 1)
	c.waiters[documentURI] = append(c.waiters[documentURI], waiter)
	c.mu.Unlock()

	select {
	case diagnostics := <-waiter:
		return &protocol.FullDocumentDiagnosticReport{Kind: string(protocol.DocumentDiagnosticReportKindFull), Items: diagnostics}, nil
	case <-ctx.Done():
		c.mu.Lock()
		waiters := c.waiters[documentURI]
		for index, candidate := range waiters {
			if candidate == waiter {
				c.waiters[documentURI] = slices.Delete(waiters, index, index+1)
				break
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (*lspProtocolClient) RegisterCapability(context.Context, *protocol.RegistrationParams) error {
	return nil
}

func (*lspProtocolClient) UnregisterCapability(context.Context, *protocol.UnregistrationParams) error {
	return nil
}

func (*lspProtocolClient) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}

func (*lspProtocolClient) Configuration(_ context.Context, params *protocol.ConfigurationParams) ([]protocol.LSPAny, error) {
	return make([]protocol.LSPAny, len(params.Items)), nil
}

func (c *lspProtocolClient) WorkspaceFolders(context.Context) ([]protocol.WorkspaceFolder, error) {
	return []protocol.WorkspaceFolder{c.folder}, nil
}

func (*lspProtocolClient) ApplyEdit(context.Context, *protocol.ApplyWorkspaceEditParams) (*protocol.ApplyWorkspaceEditResult, error) {
	reason := "server-initiated edits are not supported"
	return &protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: &reason}, nil
}

func (t *lspTransport) Read(data []byte) (int, error) {
	return t.reader.Read(data)
}

func (t *lspTransport) Write(data []byte) (int, error) {
	return t.writer.Write(data)
}

func (t *lspTransport) Close() error {
	return errors.Join(t.reader.Close(), t.writer.Close())
}

func lspServerForPath(path string) (lspServerConfig, error) {
	extension := strings.ToLower(filepath.Ext(path))
	for _, config := range lspServerConfigs {
		if slices.Contains(config.extensions, extension) {
			return config, nil
		}
	}
	return lspServerConfig{}, fmt.Errorf("no language server configured for %s", extension)
}

func readLSPDocument(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.ToSlash(path))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := validateText(content); err != nil {
		return nil, err
	}
	return content, nil
}
