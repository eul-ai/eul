package lsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/eul-ai/eul/tool/textfile"
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

type fileChange struct {
	snapshot textfile.Snapshot
	data     []byte
}

type lspPathEdits struct {
	requestedPath string
	edits         []protocol.TextEdit
	version       *int32
}

func applyWorkspaceEdit(ctx context.Context, watcher *watchManager, workspaceEdit *protocol.WorkspaceEdit, documents ...textfile.Snapshot) (int, error) {
	changes, err := planLSPWorkspaceEdit(workspaceEdit, documents...)
	if err != nil {
		return 0, err
	}
	committed, commitErr := commitLSPFileChanges(ctx, changes)
	notifyErr := notifyFileChanges(watcher, changes[:committed])
	return committed, errors.Join(commitErr, notifyErr)
}

func notifyFileChanges(watcher *watchManager, changes []fileChange) error {
	paths := make([]string, len(changes))
	for index, change := range changes {
		paths[index] = change.snapshot.Path
	}
	notifyCtx, cancel := context.WithTimeout(context.Background(), watchNotifyTimeout)
	defer cancel()
	return watcher.reportCommitted(notifyCtx, paths)
}

func planLSPWorkspaceEdit(workspaceEdit *protocol.WorkspaceEdit, documents ...textfile.Snapshot) ([]fileChange, error) {
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
		if documentEdits.version == nil {
			documentEdits.version = version
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

	documentURIs := make([]uri.URI, 0, len(editsByURI))
	for documentURI := range editsByURI {
		documentURIs = append(documentURIs, documentURI)
	}
	sort.Slice(documentURIs, func(left, right int) bool {
		return documentURIs[left].FsPath() < documentURIs[right].FsPath()
	})

	editsByPath := make(map[string]lspPathEdits, len(documentURIs))
	for _, documentURI := range documentURIs {
		if documentURI.Scheme() != "file" {
			return nil, fmt.Errorf("unsupported document URI %q", documentURI)
		}
		requestedPath := documentURI.FsPath()
		resolvedPath, err := filepath.EvalSymlinks(requestedPath)
		if err != nil {
			return nil, err
		}
		pathEdits := editsByPath[resolvedPath]
		if pathEdits.requestedPath == "" {
			pathEdits.requestedPath = requestedPath
		}
		documentEdits := editsByURI[documentURI]
		if pathEdits.version != nil && documentEdits.version != nil && *pathEdits.version != *documentEdits.version {
			return nil, fmt.Errorf("document %q has conflicting versions %d and %d", documentURI, *pathEdits.version, *documentEdits.version)
		}
		if pathEdits.version == nil {
			pathEdits.version = documentEdits.version
		}
		pathEdits.edits = append(pathEdits.edits, documentEdits.edits...)
		editsByPath[resolvedPath] = pathEdits
	}

	knownDocuments := make(map[string]textfile.Snapshot, len(documents))
	for _, document := range documents {
		knownDocuments[document.Path] = document
	}
	resolvedPaths := make([]string, 0, len(editsByPath))
	for resolvedPath := range editsByPath {
		resolvedPaths = append(resolvedPaths, resolvedPath)
	}
	sort.Strings(resolvedPaths)

	changes := make([]fileChange, 0, len(resolvedPaths))
	for _, resolvedPath := range resolvedPaths {
		pathEdits := editsByPath[resolvedPath]
		snapshot, opened := knownDocuments[resolvedPath]
		if err := validateLSPDocumentVersion(pathEdits.requestedPath, pathEdits.version, opened); err != nil {
			return nil, err
		}
		if !opened {
			var err error
			snapshot, err = textfile.Load(pathEdits.requestedPath)
			if err != nil {
				return nil, err
			}
		}
		updated, err := applyLSPTextEdits(snapshot.Data, pathEdits.edits)
		if err != nil {
			return nil, fmt.Errorf("apply edits to %s: %w", filepath.ToSlash(pathEdits.requestedPath), err)
		}
		changes = append(changes, fileChange{snapshot: snapshot, data: updated})
	}
	return changes, nil
}

func validateLSPDocumentVersion(path string, version *int32, opened bool) error {
	if version == nil {
		return nil
	}
	want := int32(0)
	kind := "unopened"
	if opened {
		want = lspDocumentVersion
		kind = "opened"
	}
	if *version != want {
		return fmt.Errorf("document %q has version %d; %s document requires version %d", filepath.ToSlash(path), *version, kind, want)
	}
	return nil
}

func commitLSPFileChanges(ctx context.Context, changes []fileChange) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	replacements := make([]*textfile.Replacement, 0, len(changes))
	defer func() {
		for _, replacement := range replacements {
			replacement.Discard()
		}
	}()
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		replacement, err := textfile.Prepare(change.snapshot, change.data)
		if err != nil {
			return 0, err
		}
		replacements = append(replacements, replacement)
	}
	for _, replacement := range replacements {
		if err := replacement.Verify(); err != nil {
			return 0, err
		}
	}
	for index, replacement := range replacements {
		if err := ctx.Err(); err != nil {
			return index, fmt.Errorf("committed %d of %d files; files may have changed: %w", index, len(replacements), err)
		}
		if err := replacement.Commit(); err != nil {
			return index, fmt.Errorf("committed %d of %d files; files may have changed: %w", index, len(replacements), err)
		}
	}
	return len(replacements), nil
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
