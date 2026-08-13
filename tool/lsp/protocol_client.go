package lsp

import (
	"context"
	"slices"
	"sync"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type protocolClient struct {
	protocol.UnimplementedClient
	folder  protocol.WorkspaceFolder
	watcher *watchManager

	diagnosticsMu sync.Mutex
	diagnostics   map[uri.URI][]protocol.Diagnostic
	waiters       map[uri.URI][]chan []protocol.Diagnostic
}

func (c *protocolClient) PublishDiagnostics(_ context.Context, params *protocol.PublishDiagnosticsParams) error {
	diagnostics := slices.Clone(params.Diagnostics)

	c.diagnosticsMu.Lock()
	c.diagnostics[params.URI] = diagnostics
	waiters := c.waiters[params.URI]
	delete(c.waiters, params.URI)
	c.diagnosticsMu.Unlock()

	for _, waiter := range waiters {
		waiter <- diagnostics
	}
	return nil
}

func (c *protocolClient) clearDiagnostics(documentURI uri.URI) {
	c.diagnosticsMu.Lock()
	delete(c.diagnostics, documentURI)
	c.diagnosticsMu.Unlock()
}

func (c *protocolClient) waitForDiagnostics(ctx context.Context, documentURI uri.URI) (*protocol.FullDocumentDiagnosticReport, error) {
	c.diagnosticsMu.Lock()
	if diagnostics, exists := c.diagnostics[documentURI]; exists {
		c.diagnosticsMu.Unlock()
		return &protocol.FullDocumentDiagnosticReport{Kind: string(protocol.DocumentDiagnosticReportKindFull), Items: diagnostics}, nil
	}
	waiter := make(chan []protocol.Diagnostic, 1)
	c.waiters[documentURI] = append(c.waiters[documentURI], waiter)
	c.diagnosticsMu.Unlock()

	select {
	case diagnostics := <-waiter:
		return &protocol.FullDocumentDiagnosticReport{Kind: string(protocol.DocumentDiagnosticReportKindFull), Items: diagnostics}, nil
	case <-ctx.Done():
		c.diagnosticsMu.Lock()
		waiters := c.waiters[documentURI]
		for index, candidate := range waiters {
			if candidate != waiter {
				continue
			}
			waiters = slices.Delete(waiters, index, index+1)
			if len(waiters) == 0 {
				delete(c.waiters, documentURI)
			} else {
				c.waiters[documentURI] = waiters
			}
			break
		}
		c.diagnosticsMu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *protocolClient) RegisterCapability(ctx context.Context, params *protocol.RegistrationParams) error {
	return c.watcher.register(ctx, params.Registrations)
}

func (c *protocolClient) UnregisterCapability(ctx context.Context, params *protocol.UnregistrationParams) error {
	return c.watcher.unregister(ctx, params.Unregisterations)
}

func (*protocolClient) WorkDoneProgressCreate(context.Context, *protocol.WorkDoneProgressCreateParams) error {
	return nil
}

func (*protocolClient) Configuration(_ context.Context, params *protocol.ConfigurationParams) ([]protocol.LSPAny, error) {
	return make([]protocol.LSPAny, len(params.Items)), nil
}

func (c *protocolClient) WorkspaceFolders(context.Context) ([]protocol.WorkspaceFolder, error) {
	return []protocol.WorkspaceFolder{c.folder}, nil
}

func (*protocolClient) ApplyEdit(context.Context, *protocol.ApplyWorkspaceEditParams) (*protocol.ApplyWorkspaceEditResult, error) {
	reason := "server-initiated edits are not supported"
	return &protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: &reason}, nil
}
