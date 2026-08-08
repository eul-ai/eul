package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"go.lsp.dev/protocol"

	"yaah/agent"
	"yaah/tool"
)

const (
	lspDiagnosticsToolName = "lsp_diagnostics"
	lspHoverToolName       = "lsp_hover"
	lspDefinitionToolName  = "lsp_definition"
	lspReferencesToolName  = "lsp_references"
	lspSymbolsToolName     = "lsp_symbols"
	lspRenameToolName      = "lsp_rename"
)

var (
	lspDiagnosticsToolDefinition = agent.ToolDefinition{
		Name:        lspDiagnosticsToolName,
		Description: "Return current language-server diagnostics for a source file.",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path": {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		}, "path"),
	}
	lspHoverToolDefinition = agent.ToolDefinition{
		Name:        lspHoverToolName,
		Description: "Return type and documentation information from the language server at a source position.",
		Parameters:  lspPositionSchema(),
	}
	lspDefinitionToolDefinition = agent.ToolDefinition{
		Name:        lspDefinitionToolName,
		Description: "Return language-server definition locations for the symbol at a source position.",
		Parameters:  lspPositionSchema(),
	}
	lspReferencesToolDefinition = agent.ToolDefinition{
		Name:        lspReferencesToolName,
		Description: "Return language-server reference locations for the symbol at a source position.",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path":               {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
			"line":               {Type: "integer", Description: "Zero-based line number."},
			"character":          {Type: "integer", Description: "Zero-based UTF-16 character offset."},
			"includeDeclaration": {Type: "boolean", Description: "Whether to include the symbol declaration; defaults to false."},
		}, "path", "line", "character"),
	}
	lspSymbolsToolDefinition = agent.ToolDefinition{
		Name:        lspSymbolsToolName,
		Description: "Return language-server document symbols for a source file.",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path": {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		}, "path"),
	}
	lspRenameToolDefinition = agent.ToolDefinition{
		Name:        lspRenameToolName,
		Description: "Rename the symbol at a source position and apply the language-server workspace edits.",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path":      {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
			"line":      {Type: "integer", Description: "Approximate zero-based line used to disambiguate oldName."},
			"character": {Type: "integer", Description: "Approximate zero-based UTF-16 character offset used to disambiguate oldName."},
			"oldName":   {Type: "string", Description: "Current symbol name."},
			"newName":   {Type: "string", Description: "New symbol name."},
		}, "path", "line", "character", "oldName", "newName"),
	}
)

type lspOperation int

const (
	lspDiagnostics lspOperation = iota
	lspHover
	lspDefinition
	lspReferences
	lspSymbols
	lspRename
)

type lspTool struct {
	client     *lspClient
	definition agent.ToolDefinition
	operation  lspOperation
}

type lspPathArguments struct {
	Path string `json:"path"`
}

type lspPositionArguments struct {
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Character *int   `json:"character"`
}

type lspReferencesArguments struct {
	Path               string `json:"path"`
	Line               *int   `json:"line"`
	Character          *int   `json:"character"`
	IncludeDeclaration bool   `json:"includeDeclaration"`
}

type Set struct {
	tools     []tool.Tool
	client    *lspClient
	closeOnce sync.Once
}

func New(cwd string) *Set {
	return newSet(cwd, true)
}

func NewReadOnly(cwd string) *Set {
	return newSet(cwd, false)
}

func newSet(cwd string, includeRename bool) *Set {
	set := &Set{}
	if !hasAvailableLSPServer() {
		return set
	}

	set.client = newLSPClient(cwd)
	set.tools = []tool.Tool{
		&lspTool{client: set.client, definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics},
		&lspTool{client: set.client, definition: lspHoverToolDefinition, operation: lspHover},
		&lspTool{client: set.client, definition: lspDefinitionToolDefinition, operation: lspDefinition},
		&lspTool{client: set.client, definition: lspReferencesToolDefinition, operation: lspReferences},
		&lspTool{client: set.client, definition: lspSymbolsToolDefinition, operation: lspSymbols},
	}
	if includeRename {
		set.tools = append(set.tools, &lspTool{client: set.client, definition: lspRenameToolDefinition, operation: lspRename})
	}
	return set
}

func (s *Set) Tools() []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

func (s *Set) Close() error {
	s.closeOnce.Do(func() {
		if s.client != nil {
			s.client.stop()
		}
	})
	return nil
}

func (t *lspTool) Definition() agent.ToolDefinition {
	return t.definition
}

func (t *lspTool) Presentation(snapshot tool.PresentationSnapshot) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: t.definition.Name}
	switch t.operation {
	case lspDiagnostics:
		presentation.Arguments, _ = snapshot.Arguments["path"].(string)
	case lspRename:
		oldName, _ := snapshot.Arguments["oldName"].(string)
		newName, _ := snapshot.Arguments["newName"].(string)
		if oldName != "" && newName != "" {
			presentation.Arguments = oldName + " → " + newName
		}
	}
	return presentation
}

func (t *lspTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	var result agent.ToolResult
	var err error
	switch t.operation {
	case lspDiagnostics, lspSymbols:
		result, err = t.executePath(ctx, arguments)
	case lspHover, lspDefinition:
		result, err = t.executePosition(ctx, arguments)
	case lspReferences:
		result, err = t.executeReferences(ctx, arguments)
	case lspRename:
		result, err = t.executeRename(ctx, arguments)
	}
	if err == nil {
		return result, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return agent.ToolResult{}, contextErr
	}
	return errorResult(t.definition.Name, err), nil
}

func (t *lspTool) executePath(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	args, err := decodeArguments[lspPathArguments](arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	path, err := t.client.workspace.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	response, err := t.client.documentRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		switch t.operation {
		case lspDiagnostics:
			return session.diagnostics(ctx, document)
		case lspSymbols:
			return session.server.DocumentSymbol(ctx, &protocol.DocumentSymbolParams{TextDocument: document})
		}
		return nil, nil
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return formatLSPResult(response)
}

func (t *lspTool) executePosition(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	args, err := decodeArguments[lspPositionArguments](arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	position, err := validLSPPosition(args.Line, args.Character)
	if err != nil {
		return agent.ToolResult{}, err
	}
	path, err := t.client.workspace.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	response, err := t.client.documentRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		params := protocol.TextDocumentPositionParams{TextDocument: document, Position: position}
		switch t.operation {
		case lspHover:
			return session.server.Hover(ctx, &protocol.HoverParams{TextDocumentPositionParams: params})
		case lspDefinition:
			return session.server.Definition(ctx, &protocol.DefinitionParams{TextDocumentPositionParams: params})
		}
		return nil, nil
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return formatLSPResult(response)
}

func (t *lspTool) executeReferences(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	args, err := decodeArguments[lspReferencesArguments](arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	position, err := validLSPPosition(args.Line, args.Character)
	if err != nil {
		return agent.ToolResult{}, err
	}
	path, err := t.client.workspace.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	response, err := t.client.documentRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		return session.server.References(ctx, &protocol.ReferenceParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: document, Position: position},
			Context:                    protocol.ReferenceContext{IncludeDeclaration: args.IncludeDeclaration},
		})
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return formatLSPResult(response)
}

func lspPositionSchema() agent.JSONSchema {
	return strictObject(map[string]agent.JSONSchema{
		"path":      {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		"line":      {Type: "integer", Description: "Zero-based line number."},
		"character": {Type: "integer", Description: "Zero-based UTF-16 character offset."},
	}, "path", "line", "character")
}

func validLSPPosition(line, character *int) (protocol.Position, error) {
	if line == nil || *line < 0 {
		return protocol.Position{}, errors.New("line is required and must be nonnegative")
	}
	if character == nil || *character < 0 {
		return protocol.Position{}, errors.New("character is required and must be nonnegative")
	}
	if uint64(*line) > uint64(^uint32(0)) || uint64(*character) > uint64(^uint32(0)) {
		return protocol.Position{}, errors.New("position exceeds LSP range")
	}
	return protocol.Position{Line: uint32(*line), Character: uint32(*character)}, nil
}

func formatLSPResult(response any) (agent.ToolResult, error) {
	encoded, err := protocol.Marshal(response)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode LSP result: %w", err)
	}

	var output bytes.Buffer
	if err := json.Indent(&output, encoded, "", "  "); err != nil {
		return successResult(string(encoded)), nil
	}
	return successResult(output.String()), nil
}
