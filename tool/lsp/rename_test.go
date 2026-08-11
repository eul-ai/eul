package lsp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.lsp.dev/protocol"
)

type renameTestServer struct {
	protocol.UnimplementedServer
	prepareResult protocol.PrepareRenameResult
	prepareErr    error
	prepareCalls  int
	renameCalls   int
}

func (s *renameTestServer) PrepareRename(context.Context, *protocol.PrepareRenameParams) (protocol.PrepareRenameResult, error) {
	s.prepareCalls++
	return s.prepareResult, s.prepareErr
}

func (s *renameTestServer) Rename(context.Context, *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.renameCalls++
	return &protocol.WorkspaceEdit{}, nil
}

func TestLSPRenameCapabilities(t *testing.T) {
	prepare := true
	noPrepare := false
	tests := []struct {
		name        string
		provider    protocol.RenameProvider
		wantRename  bool
		wantPrepare bool
	}{
		{name: "nil"},
		{name: "false", provider: protocol.Boolean(false)},
		{name: "true", provider: protocol.Boolean(true), wantRename: true},
		{name: "options without prepare", provider: &protocol.RenameOptions{}, wantRename: true},
		{name: "options with false prepare", provider: &protocol.RenameOptions{PrepareProvider: &noPrepare}, wantRename: true},
		{name: "options with prepare", provider: &protocol.RenameOptions{PrepareProvider: &prepare}, wantRename: true, wantPrepare: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rename, prepare := lspRenameCapabilities(test.provider)
			if rename != test.wantRename || prepare != test.wantPrepare {
				t.Fatalf("capabilities = rename %v, prepare %v; want rename %v, prepare %v", rename, prepare, test.wantRename, test.wantPrepare)
			}
		})
	}
}

func TestRenameSymbolRejectsUnsupportedRename(t *testing.T) {
	server := &renameTestServer{}
	result, err := renameSymbol(context.Background(), &lspSession{server: server}, protocol.TextDocumentPositionParams{}, "Number")
	if result != nil || err == nil || !strings.Contains(err.Error(), "does not support rename") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if server.prepareCalls != 0 || server.renameCalls != 0 {
		t.Fatalf("prepare calls = %d, rename calls = %d", server.prepareCalls, server.renameCalls)
	}
}

func TestRenameSymbolSkipsUnsupportedPrepare(t *testing.T) {
	server := &renameTestServer{}
	result, err := renameSymbol(context.Background(), &lspSession{server: server, renameSupported: true}, protocol.TextDocumentPositionParams{}, "Number")
	if err != nil || result == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if server.prepareCalls != 0 || server.renameCalls != 1 {
		t.Fatalf("prepare calls = %d, rename calls = %d", server.prepareCalls, server.renameCalls)
	}
}

func TestRenameSymbolRejectsNilPrepareResult(t *testing.T) {
	server := &renameTestServer{}
	result, err := renameSymbol(context.Background(), &lspSession{
		server: server, renameSupported: true, prepareRenameSupport: true,
	}, protocol.TextDocumentPositionParams{}, "Number")
	if result != nil || err == nil || !strings.Contains(err.Error(), "symbol is not renameable") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if server.prepareCalls != 1 || server.renameCalls != 0 {
		t.Fatalf("prepare calls = %d, rename calls = %d", server.prepareCalls, server.renameCalls)
	}
}

func TestRenameSymbolPropagatesPrepareFailure(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	server := &renameTestServer{prepareErr: prepareErr}
	if _, err := renameSymbol(context.Background(), &lspSession{
		server: server, renameSupported: true, prepareRenameSupport: true,
	}, protocol.TextDocumentPositionParams{}, "Number"); !errors.Is(err, prepareErr) {
		t.Fatalf("error = %v, want %v", err, prepareErr)
	}
	if server.prepareCalls != 1 || server.renameCalls != 0 {
		t.Fatalf("prepare calls = %d, rename calls = %d", server.prepareCalls, server.renameCalls)
	}
}

func TestRenameSymbolRenamesAfterPrepare(t *testing.T) {
	server := &renameTestServer{prepareResult: &protocol.Range{}}
	result, err := renameSymbol(context.Background(), &lspSession{
		server: server, renameSupported: true, prepareRenameSupport: true,
	}, protocol.TextDocumentPositionParams{}, "Number")
	if err != nil || result == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if server.prepareCalls != 1 || server.renameCalls != 1 {
		t.Fatalf("prepare calls = %d, rename calls = %d", server.prepareCalls, server.renameCalls)
	}
}
