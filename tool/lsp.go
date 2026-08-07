package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"yaah/agent"
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
	IncludeDeclaration *bool  `json:"includeDeclaration"`
}

type lspRenameArguments struct {
	Path      string  `json:"path"`
	Line      *int    `json:"line"`
	Character *int    `json:"character"`
	OldName   *string `json:"oldName"`
	NewName   *string `json:"newName"`
}

type resolvedLSPTextEdit struct {
	start   int
	end     int
	newText string
}

type lspFileChange struct {
	path string
	mode os.FileMode
	data []byte
}

func NewLSP(cwd string) []Tool {
	if !hasAvailableLSPServer() {
		return nil
	}

	client := newLSPClient(cwd)
	return []Tool{
		&lspTool{client: client, definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics},
		&lspTool{client: client, definition: lspHoverToolDefinition, operation: lspHover},
		&lspTool{client: client, definition: lspDefinitionToolDefinition, operation: lspDefinition},
		&lspTool{client: client, definition: lspReferencesToolDefinition, operation: lspReferences},
		&lspTool{client: client, definition: lspSymbolsToolDefinition, operation: lspSymbols},
		&lspTool{client: client, definition: lspRenameToolDefinition, operation: lspRename},
	}
}

func (t *lspTool) Definition() agent.ToolDefinition {
	return t.definition
}

func (t *lspTool) Close() error {
	t.client.stop()
	return nil
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

	includeDeclaration := false
	if args.IncludeDeclaration != nil {
		includeDeclaration = *args.IncludeDeclaration
	}
	response, err := t.client.documentRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		return session.server.References(ctx, &protocol.ReferenceParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: document, Position: position},
			Context:                    protocol.ReferenceContext{IncludeDeclaration: includeDeclaration},
		})
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return formatLSPResult(response)
}

func (t *lspTool) executeRename(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	args, err := decodeArguments[lspRenameArguments](arguments)
	if err != nil {
		return agent.ToolResult{}, err
	}
	hint, err := validLSPPosition(args.Line, args.Character)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if args.OldName == nil || *args.OldName == "" {
		return agent.ToolResult{}, errors.New("oldName is required and must be nonempty")
	}
	if args.NewName == nil || *args.NewName == "" {
		return agent.ToolResult{}, errors.New("newName is required and must be nonempty")
	}
	path, err := t.client.workspace.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	content, err := readLSPDocument(path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	position, err := resolveLSPRenamePosition(content, hint, *args.OldName)
	if err != nil {
		return agent.ToolResult{}, err
	}

	response, err := t.client.documentRequest(ctx, path, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		params := protocol.TextDocumentPositionParams{TextDocument: document, Position: position}
		if _, err := session.server.PrepareRename(ctx, &protocol.PrepareRenameParams{TextDocumentPositionParams: params}); err != nil {
			return nil, err
		}
		return session.server.Rename(ctx, &protocol.RenameParams{
			TextDocumentPositionParams: params,
			NewName:                    *args.NewName,
		})
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	workspaceEdit, ok := response.(*protocol.WorkspaceEdit)
	if !ok {
		return agent.ToolResult{}, fmt.Errorf("unexpected rename response %T", response)
	}
	changed, err := applyLSPWorkspaceEdit(ctx, workspaceEdit)
	if err != nil {
		return agent.ToolResult{}, err
	}

	return successResult(fmt.Sprintf("renamed symbol in %d files", changed)), nil
}

func resolveLSPRenamePosition(content []byte, hint protocol.Position, oldName string) (protocol.Position, error) {
	if strings.ContainsAny(oldName, "\r\n") {
		return protocol.Position{}, errors.New("oldName must be a single-line identifier")
	}

	var best protocol.Position
	var bestLineDistance, bestCharacterDistance uint32
	found := false
	ambiguous := false
	for lineNumber, line := range strings.Split(string(content), "\n") {
		for searchStart := 0; searchStart <= len(line)-len(oldName); {
			relativeStart := strings.Index(line[searchStart:], oldName)
			if relativeStart < 0 {
				break
			}
			start := searchStart + relativeStart
			searchStart = start + len(oldName)
			if !isIdentifierOccurrence(line, start, searchStart) {
				continue
			}

			position := protocol.Position{Line: uint32(lineNumber), Character: utf16Length(line[:start])}
			lineDistance, characterDistance := lspPositionDistance(hint, position)
			switch {
			case !found || lineDistance < bestLineDistance || lineDistance == bestLineDistance && characterDistance < bestCharacterDistance:
				best = position
				bestLineDistance = lineDistance
				bestCharacterDistance = characterDistance
				found = true
				ambiguous = false
			case lineDistance == bestLineDistance && characterDistance == bestCharacterDistance:
				ambiguous = true
			}
		}
	}
	if !found {
		return protocol.Position{}, fmt.Errorf("oldName %q was not found", oldName)
	}
	if ambiguous {
		return protocol.Position{}, fmt.Errorf("oldName %q is ambiguous near %d:%d", oldName, hint.Line, hint.Character)
	}
	return best, nil
}

func isIdentifierOccurrence(line string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(line[:start])
		if isIdentifierRune(previous) {
			return false
		}
	}
	if end < len(line) {
		next, _ := utf8.DecodeRuneInString(line[end:])
		if isIdentifierRune(next) {
			return false
		}
	}
	return true
}

func isIdentifierRune(value rune) bool {
	return value == '_' || value == '$' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func utf16Length(value string) uint32 {
	var length uint32
	for _, runeValue := range value {
		length++
		if runeValue > 0xffff {
			length++
		}
	}
	return length
}

func lspPositionDistance(left, right protocol.Position) (uint32, uint32) {
	return absoluteDifference(left.Line, right.Line), absoluteDifference(left.Character, right.Character)
}

func absoluteDifference(left, right uint32) uint32 {
	if left >= right {
		return left - right
	}
	return right - left
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

func applyLSPWorkspaceEdit(ctx context.Context, workspaceEdit *protocol.WorkspaceEdit) (int, error) {
	if workspaceEdit == nil {
		return 0, nil
	}

	editsByURI := make(map[uri.URI][]protocol.TextEdit, len(workspaceEdit.Changes))
	for documentURI, edits := range workspaceEdit.Changes {
		editsByURI[documentURI] = append(editsByURI[documentURI], edits...)
	}
	for _, change := range workspaceEdit.DocumentChanges {
		documentEdit, ok := change.(*protocol.TextDocumentEdit)
		if !ok {
			return 0, fmt.Errorf("unsupported workspace edit operation %T", change)
		}
		for _, edit := range documentEdit.Edits {
			switch edit := edit.(type) {
			case *protocol.TextEdit:
				editsByURI[documentEdit.TextDocument.URI] = append(editsByURI[documentEdit.TextDocument.URI], *edit)
			case *protocol.AnnotatedTextEdit:
				editsByURI[documentEdit.TextDocument.URI] = append(editsByURI[documentEdit.TextDocument.URI], edit.TextEdit)
			default:
				return 0, fmt.Errorf("unsupported text edit %T", edit)
			}
		}
	}

	changes := make([]lspFileChange, 0, len(editsByURI))
	for documentURI, edits := range editsByURI {
		if documentURI.Scheme() != "file" {
			return 0, fmt.Errorf("unsupported document URI %q", documentURI)
		}
		path := documentURI.FsPath()
		info, err := os.Stat(path)
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("%s is not a regular file", filepath.ToSlash(path))
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		updated, err := applyLSPTextEdits(content, edits)
		if err != nil {
			return 0, fmt.Errorf("apply edits to %s: %w", filepath.ToSlash(path), err)
		}
		changes = append(changes, lspFileChange{path: path, mode: info.Mode(), data: updated})
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for _, change := range changes {
		if err := os.WriteFile(change.path, change.data, change.mode); err != nil {
			return 0, err
		}
	}
	return len(changes), nil
}

func applyLSPTextEdits(content []byte, edits []protocol.TextEdit) ([]byte, error) {
	resolved := make([]resolvedLSPTextEdit, 0, len(edits))
	for _, edit := range edits {
		start, err := lspPositionOffset(content, edit.Range.Start)
		if err != nil {
			return nil, err
		}
		end, err := lspPositionOffset(content, edit.Range.End)
		if err != nil {
			return nil, err
		}
		if start > end {
			return nil, errors.New("text edit range starts after it ends")
		}
		resolved = append(resolved, resolvedLSPTextEdit{start: start, end: end, newText: edit.NewText})
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start > resolved[j].start
		}
		return resolved[i].end > resolved[j].end
	})

	updated := append([]byte(nil), content...)
	previousStart := len(content)
	for _, edit := range resolved {
		if edit.end > previousStart {
			return nil, errors.New("text edits overlap")
		}
		replacement := make([]byte, 0, len(updated)-(edit.end-edit.start)+len(edit.newText))
		replacement = append(replacement, updated[:edit.start]...)
		replacement = append(replacement, edit.newText...)
		replacement = append(replacement, updated[edit.end:]...)
		updated = replacement
		previousStart = edit.start
	}
	return updated, nil
}

func lspPositionOffset(content []byte, position protocol.Position) (int, error) {
	line := int(position.Line)
	character := int(position.Character)
	lineStart := 0
	for range line {
		newline := bytes.IndexByte(content[lineStart:], '\n')
		if newline < 0 {
			return 0, fmt.Errorf("line %d is beyond end of file", line)
		}
		lineStart += newline + 1
	}

	lineEnd := len(content)
	if newline := bytes.IndexByte(content[lineStart:], '\n'); newline >= 0 {
		lineEnd = lineStart + newline
		if lineEnd > lineStart && content[lineEnd-1] == '\r' {
			lineEnd--
		}
	}

	utf16Offset := 0
	for offset := lineStart; offset < lineEnd; {
		if utf16Offset == character {
			return offset, nil
		}
		runeValue, size := utf8.DecodeRune(content[offset:lineEnd])
		units := 1
		if runeValue > 0xffff {
			units = 2
		}
		if utf16Offset+units > character {
			return 0, errors.New("character points inside a UTF-16 surrogate pair")
		}
		utf16Offset += units
		offset += size
	}
	if utf16Offset == character {
		return lineEnd, nil
	}
	return 0, fmt.Errorf("character %d is beyond end of line", character)
}
