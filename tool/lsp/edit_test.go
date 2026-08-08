package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"yaah/tool/textfile"
)

func TestPlanAndCommitLSPWorkspaceEdit(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.go")
	secondPath := filepath.Join(directory, "second.go")
	if err := os.WriteFile(firstPath, []byte("alpha"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstURI := uri.File(firstPath)
	secondURI := uri.File(secondPath)
	workspaceEdit := &protocol.WorkspaceEdit{
		Changes: map[uri.URI][]protocol.TextEdit{
			firstURI: {{Range: protocol.Range{End: protocol.Position{Character: 5}}, NewText: "A"}},
		},
		DocumentChanges: []protocol.DocumentChange{
			&protocol.TextDocumentEdit{
				TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: secondURI}, Version: lspVersion(0)},
				Edits: []protocol.TextDocumentEditElement{
					&protocol.AnnotatedTextEdit{TextEdit: protocol.TextEdit{Range: protocol.Range{End: protocol.Position{Character: 4}}, NewText: "B"}},
				},
			},
		},
	}

	changes, err := planLSPWorkspaceEdit(workspaceEdit)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(changes))
	}
	resolvedFirst, _ := filepath.EvalSymlinks(firstPath)
	resolvedSecond, _ := filepath.EvalSymlinks(secondPath)
	if changes[0].snapshot.Path != resolvedFirst || changes[1].snapshot.Path != resolvedSecond {
		t.Fatalf("change order = %q, %q", changes[0].snapshot.Path, changes[1].snapshot.Path)
	}
	assertFileContent(t, firstPath, "alpha")
	assertFileContent(t, secondPath, "beta")

	if err := commitLSPFileChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, firstPath, "A")
	assertFileContent(t, secondPath, "B")
	if info, err := os.Stat(firstPath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("first mode = %v, error = %v", info.Mode().Perm(), err)
	}
}

func TestPlanLSPWorkspaceEditAcceptsGoplsVersionsForOpenedAndDiskFiles(t *testing.T) {
	directory := t.TempDir()
	openedPath := filepath.Join(directory, "opened.go")
	diskPath := filepath.Join(directory, "disk.go")
	if err := os.WriteFile(openedPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := textfile.Load(openedPath)
	if err != nil {
		t.Fatal(err)
	}
	workspaceEdit := &protocol.WorkspaceEdit{DocumentChanges: []protocol.DocumentChange{
		&protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri.File(openedPath)}, Version: lspVersion(lspDocumentVersion)},
			Edits:        []protocol.TextDocumentEditElement{&protocol.TextEdit{Range: protocol.Range{End: protocol.Position{Character: 3}}, NewText: "new"}},
		},
		&protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri.File(diskPath)}, Version: lspVersion(0)},
			Edits:        []protocol.TextDocumentEditElement{&protocol.TextEdit{Range: protocol.Range{End: protocol.Position{Character: 3}}, NewText: "new"}},
		},
	}}

	changes, err := planLSPWorkspaceEdit(workspaceEdit, opened)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(changes))
	}
	if err := commitLSPFileChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, openedPath, "new")
	assertFileContent(t, diskPath, "new")
}

func TestPlanLSPWorkspaceEditRejectsUnsupportedInputWithoutMutation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sample.go")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit *protocol.WorkspaceEdit
		want string
	}{
		{
			name: "unsupported URI",
			edit: &protocol.WorkspaceEdit{Changes: map[uri.URI][]protocol.TextEdit{
				uri.URI("https://example.test/sample.go"): nil,
			}},
			want: "unsupported document URI",
		},
		{
			name: "unsupported document operation",
			edit: &protocol.WorkspaceEdit{DocumentChanges: []protocol.DocumentChange{
				&protocol.CreateFile{Kind: "create", URI: uri.File(filepath.Join(directory, "created.go"))},
			}},
			want: "unsupported workspace edit operation",
		},
		{
			name: "overlapping edits",
			edit: &protocol.WorkspaceEdit{Changes: map[uri.URI][]protocol.TextEdit{
				uri.File(path): {
					{Range: protocol.Range{Start: protocol.Position{Character: 1}, End: protocol.Position{Character: 4}}, NewText: "x"},
					{Range: protocol.Range{Start: protocol.Position{Character: 3}, End: protocol.Position{Character: 5}}, NewText: "y"},
				},
			}},
			want: "text edits overlap",
		},
		{
			name: "conflicting document versions",
			edit: &protocol.WorkspaceEdit{DocumentChanges: []protocol.DocumentChange{
				&protocol.TextDocumentEdit{
					TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri.File(path)}, Version: lspVersion(1)},
				},
				&protocol.TextDocumentEdit{
					TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri.File(path)}, Version: lspVersion(2)},
				},
			}},
			want: "conflicting versions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := planLSPWorkspaceEdit(test.edit); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			assertFileContent(t, path, "abcdef")
		})
	}
}

func TestPlanLSPWorkspaceEditCoalescesSymlinkAliases(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sample.go")
	alias := filepath.Join(directory, "alias.go")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	workspaceEdit := &protocol.WorkspaceEdit{Changes: map[uri.URI][]protocol.TextEdit{
		uri.File(path):  {{Range: protocol.Range{End: protocol.Position{Character: 5}}, NewText: "one"}},
		uri.File(alias): {{Range: protocol.Range{Start: protocol.Position{Character: 6}, End: protocol.Position{Character: 10}}, NewText: "two"}},
	}}

	changes, err := planLSPWorkspaceEdit(workspaceEdit)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || string(changes[0].data) != "one two" {
		t.Fatalf("changes = %+v", changes)
	}
	if err := commitLSPFileChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "one two")
	if info, err := os.Lstat(alias); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("alias was not preserved: info=%v err=%v", info, err)
	}
}

func TestPlanLSPWorkspaceEditUsesOpenedDocumentSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := textfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceEdit := &protocol.WorkspaceEdit{Changes: map[uri.URI][]protocol.TextEdit{
		uri.File(path): {{Range: protocol.Range{End: protocol.Position{Character: 6}}, NewText: "after"}},
	}}
	changes, err := planLSPWorkspaceEdit(workspaceEdit, opened)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || string(changes[0].data) != "after" {
		t.Fatalf("changes = %+v", changes)
	}
	if err := commitLSPFileChanges(context.Background(), changes); !errors.Is(err, textfile.ErrChanged) {
		t.Fatalf("commit error = %v", err)
	}
	assertFileContent(t, path, "external")
}

func TestCommitLSPFileChangesRejectsChangedFileBeforeAnyCommit(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.go")
	secondPath := filepath.Join(directory, "second.go")
	if err := os.WriteFile(firstPath, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := textfile.Load(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := textfile.Load(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = commitLSPFileChanges(context.Background(), []lspFileChange{
		{snapshot: first, data: []byte("changed first")},
		{snapshot: second, data: []byte("changed second")},
	})
	if !errors.Is(err, textfile.ErrChanged) {
		t.Fatalf("commit error = %v", err)
	}
	assertFileContent(t, firstPath, "first")
	assertFileContent(t, secondPath, "external")
}

func TestCommitLSPFileChangesReportsPartialCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := textfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	err = commitLSPFileChanges(context.Background(), []lspFileChange{
		{snapshot: snapshot, data: []byte("first")},
		{snapshot: snapshot, data: []byte("second")},
	})
	if !errors.Is(err, textfile.ErrChanged) || !strings.Contains(err.Error(), "committed 1 of 2 files") {
		t.Fatalf("commit error = %v", err)
	}
	assertFileContent(t, path, "first")
}

func TestApplyLSPTextEditsPreservesSamePositionInsertOrder(t *testing.T) {
	content := []byte("ab")
	position := protocol.Position{Character: 1}
	updated, err := applyLSPTextEdits(content, []protocol.TextEdit{
		{Range: protocol.Range{Start: position, End: position}, NewText: "first"},
		{Range: protocol.Range{Start: position, End: position}, NewText: "second"},
	})
	if err != nil || string(updated) != "afirstsecondb" {
		t.Fatalf("updated=%q error=%v", updated, err)
	}
}

func TestApplyLSPTextEditsValidation(t *testing.T) {
	content := []byte("a😀b")
	for _, test := range []struct {
		name string
		edit protocol.TextEdit
		want string
	}{
		{
			name: "UTF-16 range",
			edit: protocol.TextEdit{Range: protocol.Range{Start: protocol.Position{Character: 1}, End: protocol.Position{Character: 3}}, NewText: "x"},
			want: "axb",
		},
		{
			name: "reversed range",
			edit: protocol.TextEdit{Range: protocol.Range{Start: protocol.Position{Character: 3}, End: protocol.Position{Character: 1}}, NewText: "x"},
		},
		{
			name: "inside surrogate pair",
			edit: protocol.TextEdit{Range: protocol.Range{Start: protocol.Position{Character: 2}, End: protocol.Position{Character: 3}}, NewText: "x"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := applyLSPTextEdits(content, []protocol.TextEdit{test.edit})
			if test.want == "" {
				if err == nil {
					t.Fatalf("updated = %q, want error", updated)
				}
				return
			}
			if err != nil || string(updated) != test.want {
				t.Fatalf("updated=%q error=%v, want %q", updated, err, test.want)
			}
		})
	}
}

func TestCommitLSPFileChangesHonorsCancellationAndWriteErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := textfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := commitLSPFileChanges(ctx, []lspFileChange{{snapshot: snapshot, data: []byte("after")}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit error = %v", err)
	}
	assertFileContent(t, path, "before")

	directory := t.TempDir()
	if err := commitLSPFileChanges(context.Background(), []lspFileChange{{snapshot: textfile.Snapshot{RequestedPath: directory, Path: directory, Mode: 0o644}, data: []byte("after")}}); err == nil {
		t.Fatal("commit to directory succeeded")
	}
}

func lspVersion(value int32) *int32 {
	return &value
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("%s = %q, want %q", filepath.ToSlash(path), content, want)
	}
}
