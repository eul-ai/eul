package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"go.lsp.dev/protocol"

	"github.com/eul-ai/eul/tool"
)

type Set struct {
	service *service
	tools   []tool.Tool
}

type service struct {
	client    *client
	closeOnce sync.Once
}

func New(cwd, home string, includeRename bool) (*Set, error) {
	configPaths := make([]string, 0, 2)
	if home != "" {
		configPaths = append(configPaths, filepath.Join(home, configFileName))
	}
	configPaths = append(configPaths, filepath.Join(cwd, configFileName))

	configs, err := loadServerConfigs(configPaths...)
	if err != nil {
		return nil, err
	}

	service := &service{}
	if hasAvailableServer(configs) {
		service.client = newClient(cwd, configs)
	}
	return &Set{service: service, tools: newTools(service, includeRename)}, nil
}

func (set *Set) Tools() []tool.Tool {
	return append([]tool.Tool(nil), set.tools...)
}

func (set *Set) Close() error {
	return set.service.close()
}

func (s *service) available() bool {
	return s.client != nil
}

func (s *service) close() error {
	s.closeOnce.Do(func() {
		if s.client != nil {
			s.client.stop()
		}
	})
	return nil
}

func (s *service) diagnostics(ctx context.Context, path string) (any, error) {
	return s.pathRequest(ctx, path, func(ctx context.Context, session *session, document protocol.TextDocumentIdentifier) (any, error) {
		return session.diagnostics(ctx, document)
	})
}

func (s *service) symbols(ctx context.Context, path string) (any, error) {
	return s.pathRequest(ctx, path, func(ctx context.Context, session *session, document protocol.TextDocumentIdentifier) (any, error) {
		return session.server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{TextDocument: document})
	})
}

func (s *service) hover(ctx context.Context, path string, line, character int) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *session, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.Hover(ctx, &protocol.HoverParams{TextDocumentPositionParams: params})
	})
}

func (s *service) definition(ctx context.Context, path string, line, character int) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *session, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.Definition(ctx, &protocol.DefinitionParams{TextDocumentPositionParams: params})
	})
}

func (s *service) references(ctx context.Context, path string, line, character int, includeDeclaration bool) (any, error) {
	return s.positionRequest(ctx, path, line, character, func(ctx context.Context, session *session, params protocol.TextDocumentPositionParams) (any, error) {
		return session.server.References(ctx, &protocol.ReferenceParams{
			TextDocumentPositionParams: params,
			Context:                    protocol.ReferenceContext{IncludeDeclaration: includeDeclaration},
		})
	})
}

func (s *service) pathRequest(ctx context.Context, path string, request documentRequest) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.client == nil {
		return nil, errors.New("no language server is available")
	}

	s.client.requestMu.Lock()
	defer s.client.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolved, err := s.client.workspace.resolve(path)
	if err != nil {
		return nil, err
	}
	return s.client.documentRequest(ctx, resolved, request)
}

type positionRequest func(context.Context, *session, protocol.TextDocumentPositionParams) (any, error)

func (s *service) positionRequest(ctx context.Context, path string, line, character int, request positionRequest) (any, error) {
	position, err := validPosition(line, character)
	if err != nil {
		return nil, err
	}
	return s.pathRequest(ctx, path, func(ctx context.Context, session *session, document protocol.TextDocumentIdentifier) (any, error) {
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

func (s *service) renameSymbol(ctx context.Context, path string, line, character int, oldName, newName string) (int, error) {
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

	s.client.requestMu.Lock()
	defer s.client.requestMu.Unlock()
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
