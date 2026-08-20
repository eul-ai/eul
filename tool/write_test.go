package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteCreatesParentsOverwritesAndPreservesMode(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)

	result := executeJSON(t, writeTool, map[string]any{"path": "nested/file.txt", "content": "first"})
	if result.IsError {
		t.Fatalf("initial write = %+v", result)
	}
	path := filepath.Join(cwd, "nested", "file.txt")
	if got := mustReadFile(t, path); got != "first" {
		t.Fatalf("written content = %q", got)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result = executeJSON(t, writeTool, map[string]any{"path": "nested/file.txt", "content": ""})
	if result.IsError || mustReadFile(t, path) != "" {
		t.Fatalf("empty overwrite = %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after overwrite = %o", info.Mode().Perm())
	}
}

func TestWritePresentationStreamsCollapsiblePreviewWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)
	content := strings.Join([]string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
	}, "\n")
	raw := `{"path":"preview.txt","content":"` + strings.ReplaceAll(content, "\n", `\n`)
	snapshot := PresentationSnapshot{
		ID: "write-1", Name: "write", RawArguments: raw,
		Arguments: map[string]any{"path": "preview.txt", "content": content},
	}
	presentation := writeTool.Presentation(snapshot)
	if presentation.Title != "write" || presentation.Arguments != "preview.txt" || presentation.LinesTruncated || presentation.HeadLines != writePresentationLines || !slices.Equal(presentation.Lines, strings.Split(content, "\n")) {
		t.Fatalf("presentation = %+v", presentation)
	}
	if _, err := os.Stat(filepath.Join(cwd, "preview.txt")); !os.IsNotExist(err) {
		t.Fatalf("presentation changed filesystem: %v", err)
	}

	result := executeJSON(t, writeTool, map[string]any{"path": "preview.txt", "content": content})
	if result.IsError || mustReadFile(t, filepath.Join(cwd, "preview.txt")) != content {
		t.Fatalf("write result=%+v", result)
	}
}

func TestWriteCancellationAfterNonTransactionalWriteIsFatal(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)
	ctx := &cancelAfterChecksContext{cancelAfter: 3}

	result, err := writeTool.Execute(ctx, json.RawMessage(`{"path":"file.txt","content":"written"}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write cancellation error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("write cancellation result = %+v", result)
	}
	if got := mustReadFile(t, filepath.Join(cwd, "file.txt")); got != "written" {
		t.Fatalf("non-transactional write content = %q", got)
	}
}

func TestWriteFollowsRegularSymlinkAndRejectsInvalidTargets(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "target.txt"), "old", 0o640)
	if err := os.Symlink("target.txt", filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.txt", filepath.Join(cwd, "dangling.txt")); err != nil {
		t.Fatal(err)
	}
	writeTool := NewWrite(cwd)

	if result := executeJSON(t, writeTool, map[string]any{"path": "link.txt", "content": "new"}); result.IsError {
		t.Fatalf("symlink write = %+v", result)
	}
	if got := mustReadFile(t, filepath.Join(cwd, "target.txt")); got != "new" {
		t.Fatalf("symlink target = %q", got)
	}
	for _, args := range []map[string]any{
		{"path": "dangling.txt", "content": "no"},
		{"path": ".", "content": "no"},
		{"path": "missing-content.txt"},
	} {
		result := executeJSON(t, writeTool, args)
		if !result.IsError {
			t.Fatalf("invalid write succeeded: args=%v result=%+v", args, result)
		}
	}
	if _, err := os.Lstat(filepath.Join(cwd, "missing-content.txt")); !os.IsNotExist(err) {
		t.Fatalf("missing content changed filesystem: %v", err)
	}
}
