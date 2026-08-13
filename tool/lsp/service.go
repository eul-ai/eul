package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"go.lsp.dev/protocol"
)

type Service struct {
	client    *lspClient
	closeOnce sync.Once
}

func New(cwd, home string) (*Service, error) {
	configPaths := make([]string, 0, 2)
	if home != "" {
		configPaths = append(configPaths, filepath.Join(home, lspConfigFileName))
	}
	configPaths = append(configPaths, filepath.Join(cwd, lspConfigFileName))

	configs, err := loadLSPServerConfigs(configPaths...)
	if err != nil {
		return nil, err
	}

	service := &Service{}
	if hasAvailableLSPServer(configs) {
		service.client = newLSPClient(cwd, configs)
	}
	return service, nil
}

func (s *Service) Available() bool {
	return s.client != nil
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		if s.client != nil {
			s.client.stop()
		}
	})
	return nil
}

func (s *Service) Diagnostics(ctx context.Context, path string) (any, error) {
	return s.pathRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		return session.diagnostics(ctx, document)
	})
}

func (s *Service) Symbols(ctx context.Context, path string) (any, error) {
	return s.pathRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		return session.server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{TextDocument: document})
	})
}

func (s *Service) Hover(ctx context.Context, path string, line, character int) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *lspSession, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.Hover(ctx, &protocol.HoverParams{TextDocumentPositionParams: params})
	})
}

func (s *Service) Definition(ctx context.Context, path string, line, character int) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *lspSession, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.Definition(ctx, &protocol.DefinitionParams{TextDocumentPositionParams: params})
	})
}

func (s *Service) References(ctx context.Context, path string, line, character int, includeDeclaration bool) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *lspSession, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.References(ctx, &protocol.ReferenceParams{
			TextDocumentPositionParams: params,
			Context:                    protocol.ReferenceContext{IncludeDeclaration: includeDeclaration},
		})
	})
}

func (s *Service) pathRequest(ctx context.Context, path string, request lspDocumentRequest) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, errors.New("no language server is available")
	}

	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolved, err := s.client.workspace.resolve(path)
	if err != nil {
		return nil, err
	}
	return s.client.documentRequest(ctx, resolved, request)
}

type positionRequest func(context.Context, *lspSession, protocol.TextDocumentPositionParams) (any, error)

func (s *Service) positionRequest(ctx context.Context, path string, line, character int, request positionRequest) (any, error) {
	position, err := validPosition(line, character)
	if err != nil {
		return nil, err
	}
	return s.pathRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		return request(ctx, session, protocol.TextDocumentPositionParams{TextDocument: document, Position: position})
	})
}

func validPosition(line, character int) (protocol.Position, error) {
	if line < 0 {
		return protocol.Position{}, errors.New("line is required and must be nonnegative")
	}
	if character < 0 {
		return protocol.Position{}, errors.New("character is required and must be nonnegative")
	}
	if uint64(line) > uint64(^uint32(0)) || uint64(character) > uint64(^uint32(0)) {
		return protocol.Position{}, errors.New("position exceeds LSP range")
	}
	return protocol.Position{Line: uint32(line), Character: uint32(character)}, nil
}

func (s *Service) Rename(ctx context.Context, path string, line, character int, oldName, newName string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.client == nil {
		return 0, errors.New("no language server is available")
	}

	hint, err := validPosition(line, character)
	if err != nil {
		return 0, err
	}
	if oldName == "" {
		return 0, errors.New("oldName is required and must be nonempty")
	}
	if newName == "" {
		return 0, errors.New("newName is required and must be nonempty")
	}

	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	resolved, err := s.client.workspace.resolve(path)
	if err != nil {
		return 0, err
	}
	return s.rename(ctx, resolved, hint, oldName, newName)
}

func unexpectedRenameResponse(response any) error {
	return fmt.Errorf("unexpected rename response %T", response)
}
