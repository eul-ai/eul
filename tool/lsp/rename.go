package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.lsp.dev/protocol"

	"yaah/agent"
	"yaah/tool/textfile"
)

type lspRenameArguments struct {
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Character *int   `json:"character"`
	OldName   string `json:"oldName"`
	NewName   string `json:"newName"`
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
	if args.OldName == "" {
		return agent.ToolResult{}, errors.New("oldName is required and must be nonempty")
	}
	if args.NewName == "" {
		return agent.ToolResult{}, errors.New("newName is required and must be nonempty")
	}
	path, err := t.client.workspace.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	document, err := textfile.Load(path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	position, err := resolveLSPRenamePosition(document.Data, hint, args.OldName)
	if err != nil {
		return agent.ToolResult{}, err
	}

	var watcher *lspWatchManager
	response, err := t.client.documentSnapshotRequest(ctx, document, func(ctx context.Context, session *lspSession, document protocol.TextDocumentIdentifier) (any, error) {
		watcher = session.client.watcher
		params := protocol.TextDocumentPositionParams{TextDocument: document, Position: position}
		return prepareAndRename(ctx, session.server, params, args.NewName)
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	workspaceEdit, ok := response.(*protocol.WorkspaceEdit)
	if !ok {
		return agent.ToolResult{}, fmt.Errorf("unexpected rename response %T", response)
	}
	changed, err := applyLSPWorkspaceEdit(ctx, watcher, workspaceEdit, document)
	if err != nil {
		return agent.ToolResult{}, err
	}

	return successResult(fmt.Sprintf("renamed symbol in %d files", changed)), nil
}

func prepareAndRename(ctx context.Context, server protocol.Server, params protocol.TextDocumentPositionParams, newName string) (*protocol.WorkspaceEdit, error) {
	prepared, err := server.PrepareRename(ctx, &protocol.PrepareRenameParams{TextDocumentPositionParams: params})
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("symbol is not renameable")
	}
	return server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: params,
		NewName:                    newName,
	})
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
			lineDistance := absoluteDifference(hint.Line, position.Line)
			characterDistance := absoluteDifference(hint.Character, position.Character)
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

func absoluteDifference(left, right uint32) uint32 {
	if left >= right {
		return left - right
	}
	return right - left
}
