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

func TestInsertPlacesContentAtRequestedAnchor(t *testing.T) {
	for _, test := range []struct {
		name     string
		original string
		args     map[string]any
		want     string
	}{
		{
			name:     "append null anchor",
			original: "one\n",
			args:     map[string]any{"path": "sample.txt", "content": "two\n", "anchor": nil, "position": "after"},
			want:     "one\ntwo\n",
		},
		{
			name:     "prepend",
			original: "one\n",
			args:     map[string]any{"path": "sample.txt", "content": "zero\n", "anchor": nil, "position": "before"},
			want:     "zero\none\n",
		},
		{
			name:     "before anchor",
			original: "one\nthree\n",
			args:     map[string]any{"path": "sample.txt", "content": "two\n", "anchor": "three\n", "position": "before"},
			want:     "one\ntwo\nthree\n",
		},
		{
			name:     "after anchor",
			original: "one\nthree\n",
			args:     map[string]any{"path": "sample.txt", "content": "two\n", "anchor": "one\n", "position": "after"},
			want:     "one\ntwo\nthree\n",
		},
		{
			name:     "after unterminated anchor",
			original: "one",
			args:     map[string]any{"path": "sample.txt", "content": "\ntwo", "anchor": "one", "position": "after"},
			want:     "one\ntwo",
		},
		{
			name: "empty file",
			args: map[string]any{"path": "sample.txt", "content": "one", "anchor": nil, "position": "after"},
			want: "one",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "sample.txt")
			mustWriteFile(t, path, test.original, 0o640)

			result := executeJSON(t, NewInsert(cwd), test.args)
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
		json.RawMessage(`{"path":"sample.txt","content":"two\n","anchor":"three\n","position":"before"}`),
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
	if presentation.Title != insertToolName || presentation.Arguments != "sample.txt" || !slices.Equal(presentation.Diff, wantDiff) {
		t.Fatalf("insert presentation = %+v, want diff %+v", presentation, wantDiff)
	}
}

func TestFailedInsertsNeverModifyFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	original := "one\ntwo\none\n"
	mustWriteFile(t, path, original, 0o640)
	insertTool := NewInsert(cwd)

	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing anchor", args: map[string]any{"path": "sample.txt", "content": "new\n", "anchor": "missing", "position": "before"}},
		{name: "multiple anchors", args: map[string]any{"path": "sample.txt", "content": "new\n", "anchor": "one", "position": "after"}},
		{name: "empty anchor", args: map[string]any{"path": "sample.txt", "content": "new\n", "anchor": "", "position": "before"}},
		{name: "invalid position", args: map[string]any{"path": "sample.txt", "content": "new\n", "anchor": nil, "position": "middle"}},
		{name: "missing position", args: map[string]any{"path": "sample.txt", "content": "new\n", "anchor": nil}},
		{name: "binary content", args: map[string]any{"path": "sample.txt", "content": "new\x00", "anchor": nil, "position": "after"}},
		{name: "missing content", args: map[string]any{"path": "sample.txt", "anchor": nil, "position": "after"}},
		{name: "unknown field", args: map[string]any{"path": "sample.txt", "content": "new", "anchor": nil, "position": "after", "extra": true}},
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
