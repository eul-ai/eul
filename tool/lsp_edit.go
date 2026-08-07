package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type resolvedLSPTextEdit struct {
	start   int
	end     int
	newText string
	order   int
}

type lspDocumentEdits struct {
	edits   []protocol.TextEdit
	version *int32
}

type lspFileChange struct {
	path string
	mode os.FileMode
	data []byte
}

func applyLSPWorkspaceEdit(ctx context.Context, workspaceEdit *protocol.WorkspaceEdit) (int, error) {
	changes, err := planLSPWorkspaceEdit(workspaceEdit)
	if err != nil {
		return 0, err
	}
	if err := commitLSPFileChanges(ctx, changes); err != nil {
		return 0, err
	}
	return len(changes), nil
}

func planLSPWorkspaceEdit(workspaceEdit *protocol.WorkspaceEdit) ([]lspFileChange, error) {
	if workspaceEdit == nil {
		return nil, nil
	}

	editsByURI := make(map[uri.URI]lspDocumentEdits, len(workspaceEdit.Changes))
	for documentURI, edits := range workspaceEdit.Changes {
		editsByURI[documentURI] = lspDocumentEdits{edits: append([]protocol.TextEdit(nil), edits...)}
	}
	for _, change := range workspaceEdit.DocumentChanges {
		documentEdit, ok := change.(*protocol.TextDocumentEdit)
		if !ok {
			return nil, fmt.Errorf("unsupported workspace edit operation %T", change)
		}

		documentURI := documentEdit.TextDocument.URI
		documentEdits := editsByURI[documentURI]
		version := documentEdit.TextDocument.Version
		if documentEdits.version != nil && version != nil && *documentEdits.version != *version {
			return nil, fmt.Errorf("document %q has conflicting versions %d and %d", documentURI, *documentEdits.version, *version)
		}
		if version != nil && *version != lspDocumentVersion {
			return nil, fmt.Errorf("document %q has version %d; expected %d", documentURI, *version, lspDocumentVersion)
		}
		if documentEdits.version == nil && version != nil {
			value := *version
			documentEdits.version = &value
		}

		for _, edit := range documentEdit.Edits {
			switch edit := edit.(type) {
			case *protocol.TextEdit:
				documentEdits.edits = append(documentEdits.edits, *edit)
			case *protocol.AnnotatedTextEdit:
				documentEdits.edits = append(documentEdits.edits, edit.TextEdit)
			default:
				return nil, fmt.Errorf("unsupported text edit %T", edit)
			}
		}
		editsByURI[documentURI] = documentEdits
	}

	changes := make([]lspFileChange, 0, len(editsByURI))
	for documentURI, documentEdits := range editsByURI {
		if documentURI.Scheme() != "file" {
			return nil, fmt.Errorf("unsupported document URI %q", documentURI)
		}
		path := documentURI.FsPath()
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
		updated, err := applyLSPTextEdits(content, documentEdits.edits)
		if err != nil {
			return nil, fmt.Errorf("apply edits to %s: %w", filepath.ToSlash(path), err)
		}
		changes = append(changes, lspFileChange{path: path, mode: info.Mode(), data: updated})
	}
	return changes, nil
}

func commitLSPFileChanges(ctx context.Context, changes []lspFileChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, change := range changes {
		if err := os.WriteFile(change.path, change.data, change.mode); err != nil {
			return err
		}
	}
	return nil
}

func applyLSPTextEdits(content []byte, edits []protocol.TextEdit) ([]byte, error) {
	resolved := make([]resolvedLSPTextEdit, 0, len(edits))
	for order, edit := range edits {
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
		resolved = append(resolved, resolvedLSPTextEdit{start: start, end: end, newText: edit.NewText, order: order})
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].start != resolved[j].start {
			return resolved[i].start > resolved[j].start
		}
		if resolved[i].end != resolved[j].end {
			return resolved[i].end > resolved[j].end
		}
		return resolved[i].order > resolved[j].order
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
