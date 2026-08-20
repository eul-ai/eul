package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestInsertPlacesContentAtRequestedBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		original string
		content  string
		line     any
		want     string
	}{
		{name: "append omitted", original: "one\n", content: "two\n", want: "one\ntwo\n"},
		{name: "append null", original: "one\n", content: "two\n", line: nil, want: "one\ntwo\n"},
		{name: "prepend", original: "one\n", content: "zero\n", line: 0, want: "zero\none\n"},
		{name: "middle", original: "one\nthree\n", content: "two\n", line: 1, want: "one\ntwo\nthree\n"},
		{name: "after final terminated line", original: "one\ntwo\n", content: "three\n", line: 2, want: "one\ntwo\nthree\n"},
		{name: "after final unterminated line", original: "one", content: "\ntwo", line: 1, want: "one\ntwo"},
		{name: "empty file", content: "one", line: 0, want: "one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "sample.txt")
			mustWriteFile(t, path, test.original, 0o640)
			arguments := map[string]any{"path": "sample.txt", "content": test.content}
			if test.line != nil || test.name == "append null" {
				arguments["line"] = test.line
			}

			result := executeJSON(t, NewInsert(cwd), arguments)
			if result.IsError || mustReadFile(t, path) != test.want {
				t.Fatalf("insert = %+v, content = %q, want %q", result, mustReadFile(t, path), test.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("mode after insert = %o", info.Mode().Perm())
			}
		})
	}
}

func TestInsertPublishesDiffAfterSuccessfulCommit(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	mustWriteFile(t, path, "one\nthree\n", 0o600)
	insertTool := NewInsert(cwd)

	var presentation agent.ToolPresentation
	result, err := insertTool.Execute(
		context.Background(),
		json.RawMessage(`{"path":"sample.txt","content":"two\n","line":1}`),
		toolUpdateSinkFunc(func(update agent.ToolPresentation) error {
			if got := mustReadFile(t, path); got != "one\ntwo\nthree\n" {
				t.Fatalf("diff published before commit: content = %q", got)
			}
			presentation = update
			return nil
		}),
	)
	if err != nil || result.IsError {
		t.Fatalf("insert result = %+v, error = %v", result, err)
	}

	wantDiff := []agent.ToolDiffLine{
		{Kind: agent.ToolDiffLineContext, OldLine: 1, NewLine: 1, Text: "one"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 2, Text: "two"},
		{Kind: agent.ToolDiffLineContext, OldLine: 2, NewLine: 3, Text: "three"},
	}
	if presentation.Title != insertToolName || presentation.Arguments != "sample.txt:1" || !slices.Equal(presentation.Diff, wantDiff) {
		t.Fatalf("insert presentation = %+v, want diff %+v", presentation, wantDiff)
	}
}

func TestFailedInsertsNeverModifyFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	original := "one\ntwo\n"
	mustWriteFile(t, path, original, 0o640)
	insertTool := NewInsert(cwd)

	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "negative line", args: map[string]any{"path": "sample.txt", "content": "new\n", "line": -1}},
		{name: "line beyond file", args: map[string]any{"path": "sample.txt", "content": "new\n", "line": 3}},
		{name: "binary content", args: map[string]any{"path": "sample.txt", "content": "new\x00", "line": nil}},
		{name: "missing content", args: map[string]any{"path": "sample.txt", "line": nil}},
		{name: "unknown field", args: map[string]any{"path": "sample.txt", "content": "new", "line": nil, "extra": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			finalPublished := false
			arguments, err := json.Marshal(test.args)
			if err != nil {
				t.Fatal(err)
			}
			result, err := insertTool.Execute(context.Background(), arguments, toolUpdateSinkFunc(func(agent.ToolPresentation) error {
				finalPublished = true
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || finalPublished {
				t.Fatalf("insert failure = %+v, final published=%t", result, finalPublished)
			}
			if got := mustReadFile(t, path); got != original {
				t.Fatalf("failed insert changed content to %q", got)
			}
		})
	}
}
