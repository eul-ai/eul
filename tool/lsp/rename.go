package lsp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.lsp.dev/protocol"

	"github.com/eul-ai/eul/tool/textfile"
)

func (s *service) rename(ctx context.Context, path string, hint protocol.Position, oldName, newName string) (int, error) {
	document, err := textfile.Load(path)
	if err != nil {
		return 0, err
	}
	position, err := resolveRenamePosition(document.Data, hint, oldName)
	if err != nil {
		return 0, err
	}

	var watcher *watchManager
	response, err := s.client.documentSnapshotRequest(ctx, document, func(ctx context.Context, session *session, document protocol.TextDocumentIdentifier) (any, error) {
		watcher = session.client.watcher
		params := protocol.TextDocumentPositionParams{TextDocument: document, Position: position}
		return renameSymbol(ctx, session, params, newName)
	})
	if err != nil {
		return 0, err
	}
	workspaceEdit, ok := response.(*protocol.WorkspaceEdit)
	if !ok {
		return 0, unexpectedRenameResponse(response)
	}
	return applyWorkspaceEdit(ctx, watcher, workspaceEdit, document)
}

func renameSymbol(ctx context.Context, session *session, params protocol.TextDocumentPositionParams, newName string) (*protocol.WorkspaceEdit, error) {
	if !session.renameSupported {
		return nil, errors.New("language server does not support rename")
	}
	if session.prepareRenameSupport {
		prepared, err := session.server.PrepareRename(ctx, &protocol.PrepareRenameParams{TextDocumentPositionParams: params})
		if err != nil {
			return nil, err
		}
		if prepared == nil {
			return nil, errors.New("symbol is not renameable")
		}
	}
	return session.server.Rename(ctx, &protocol.RenameParams{
		TextDocumentPositionParams: params,
		NewName:                    newName,
	})
}

func resolveRenamePosition(content []byte, hint protocol.Position, oldName string) (protocol.Position, error) {
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
