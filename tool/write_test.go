package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestWritePresentationStreamsBoundedPreviewWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)
	content := strings.Join([]string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
	}, "\n")
	raw := `{"path":"preview.txt","content":"` + strings.ReplaceAll(content, "\n", `\n`)
	presentation := writeTool.Presentation(raw)
	if presentation.Title != "write" || presentation.Arguments != "preview.txt" || !presentation.LinesTruncated || len(presentation.Lines) != 11 || presentation.Lines[0] != "one" || presentation.Lines[9] != "ten" {
		t.Fatalf("presentation = %+v", presentation)
	}
	if _, err := os.Stat(filepath.Join(cwd, "preview.txt")); !os.IsNotExist(err) {
		t.Fatalf("presentation changed filesystem: %v", err)
	}

	huge := strings.Repeat("界", writePresentationMaxBytes)
	hugeArguments, err := json.Marshal(map[string]any{"path": "huge.txt", "content": huge})
	if err != nil {
		t.Fatal(err)
	}
	hugePresentation := writeTool.Presentation(string(hugeArguments))
	if len(strings.Join(hugePresentation.Lines, "\n")) > writePresentationMaxBytes || !hugePresentation.LinesTruncated {
		t.Fatalf("huge presentation bytes=%d lines=%+v", len(strings.Join(hugePresentation.Lines, "\n")), hugePresentation.Lines)
	}

	result := executeJSON(t, writeTool, map[string]any{"path": "preview.txt", "content": content})
	if result.IsError || mustReadFile(t, filepath.Join(cwd, "preview.txt")) != content {
		t.Fatalf("write result=%+v", result)
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
