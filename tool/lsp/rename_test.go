package lsp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.lsp.dev/protocol"
)

type notRenameableServer struct {
	protocol.UnimplementedServer
	renameCalls int
}

func (*notRenameableServer) PrepareRename(context.Context, *protocol.PrepareRenameParams) (protocol.PrepareRenameResult, error) {
	return nil, nil
}

func (s *notRenameableServer) Rename(context.Context, *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	s.renameCalls++
	return &protocol.WorkspaceEdit{}, nil
}

func TestPrepareAndRenameRejectsNilPrepareResult(t *testing.T) {
	server := &notRenameableServer{}
	result, err := prepareAndRename(context.Background(), server, protocol.TextDocumentPositionParams{}, "Number")
	if result != nil || err == nil || !strings.Contains(err.Error(), "symbol is not renameable") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if server.renameCalls != 0 {
		t.Fatalf("Rename() calls = %d, want 0", server.renameCalls)
	}
}

func TestPrepareAndRenamePropagatesPrepareFailure(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	server := prepareRenameErrorServer{prepareErr: prepareErr}
	if _, err := prepareAndRename(context.Background(), server, protocol.TextDocumentPositionParams{}, "Number"); !errors.Is(err, prepareErr) {
		t.Fatalf("error = %v, want %v", err, prepareErr)
	}
}

type prepareRenameErrorServer struct {
	protocol.UnimplementedServer
	prepareErr error
}

func (s prepareRenameErrorServer) PrepareRename(context.Context, *protocol.PrepareRenameParams) (protocol.PrepareRenameResult, error) {
	return nil, s.prepareErr
}
