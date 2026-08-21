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

func TestLineInsertPlacesAndIndentsContent(t *testing.T) {
	for _, test := range []struct {
		name     string
		tool     func(string) Tool
		original string
		anchor   string
		content  string
		want     string
	}{
		{
			name:     "before anchored line",
			tool:     func(cwd string) Tool { return NewInsertBefore(cwd) },
			original: "if ready {\n\tdelete(value)\n}\n",
			anchor:   "delete(value)",
			content:  "        log(value)",
			want:     "if ready {\n\tlog(value)\n\tdelete(value)\n}\n",
		},
		{
			name:     "after anchored line",
			tool:     func(cwd string) Tool { return NewInsertAfter(cwd) },
			original: "\tone\n\tthree\n",
			anchor:   "one",
			content:  "two",
			want:     "\tone\n\ttwo\n\tthree\n",
		},
		{
			name:     "after uses following line indentation",
			tool:     func(cwd string) Tool { return NewInsertAfter(cwd) },
			original: "if ready {\n\twork()\n}\n",
			anchor:   "if ready",
			content:  "log()",
			want:     "if ready {\n\tlog()\n\twork()\n}\n",
		},
		{
			name:     "before beginning",
			tool:     func(cwd string) Tool { return NewInsertBefore(cwd) },
			original: "two\n",
			content:  "one",
			want:     "one\ntwo\n",
		},
		{
			name:     "after end",
			tool:     func(cwd string) Tool { return NewInsertAfter(cwd) },
			original: "one\n",
			content:  "two",
			want:     "one\ntwo\n",
		},
		{
			name:     "after final anchor uses anchor indentation",
			tool:     func(cwd string) Tool { return NewInsertAfter(cwd) },
			original: "\tone",
			anchor:   "one",
			content:  "two",
			want:     "\tone\n\ttwo\n",
		},
		{
			name:     "after unterminated end",
			tool:     func(cwd string) Tool { return NewInsertAfter(cwd) },
			original: "one",
			content:  "two",
			want:     "one\ntwo\n",
		},
		{
			name:     "multiline content keeps relative indentation",
			tool:     func(cwd string) Tool { return NewInsertBefore(cwd) },
			original: "\tnext()\n",
			anchor:   "next()",
			content:  "    if ready {\n        run()\n    }\n",
			want:     "\tif ready {\n\t    run()\n\t}\n\tnext()\n",
		},
		{
			name:     "preserves CRLF",
			tool:     func(cwd string) Tool { return NewInsertBefore(cwd) },
			original: "one\r\nthree\r\n",
			anchor:   "three",
			content:  "two\n",
			want:     "one\r\ntwo\r\nthree\r\n",
		},
		{
			name:    "empty file before",
			tool:    func(cwd string) Tool { return NewInsertBefore(cwd) },
			content: "one",
			want:    "one\n",
		},
		{
			name:    "empty file after",
			tool:    func(cwd string) Tool { return NewInsertAfter(cwd) },
			content: "one",
			want:    "one\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "sample.txt")
			mustWriteFile(t, path, test.original, 0o640)

			result := executeJSON(t, test.tool(cwd), map[string]any{
				"path": "sample.txt", "anchor": test.anchor, "content": test.content,
			})
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
	insertTool := NewInsertBefore(cwd)

	var presentation agent.ToolPresentation
	result, err := insertTool.Execute(
		context.Background(),
		json.RawMessage(`{"path":"sample.txt","anchor":"three","content":"two"}`),
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
	if presentation.Title != insertBeforeToolName || presentation.Arguments != "sample.txt" || !slices.Equal(presentation.Diff, wantDiff) {
		t.Fatalf("insert presentation = %+v, want diff %+v", presentation, wantDiff)
	}
}

func TestFailedLineInsertsNeverModifyFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	original := "one\ntwo\none\n"
	mustWriteFile(t, path, original, 0o640)

	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing anchor", args: map[string]any{"path": "sample.txt", "content": "new"}},
		{name: "null anchor", args: map[string]any{"path": "sample.txt", "anchor": nil, "content": "new"}},
		{name: "anchor spans lines", args: map[string]any{"path": "sample.txt", "anchor": "one\ntwo", "content": "new"}},
		{name: "anchor not found", args: map[string]any{"path": "sample.txt", "anchor": "missing", "content": "new"}},
		{name: "anchor identifies multiple lines", args: map[string]any{"path": "sample.txt", "anchor": "one", "content": "new"}},
		{name: "binary content", args: map[string]any{"path": "sample.txt", "anchor": "two", "content": "new\x00"}},
		{name: "missing content", args: map[string]any{"path": "sample.txt", "anchor": "two"}},
		{name: "unknown field", args: map[string]any{"path": "sample.txt", "anchor": "two", "content": "new", "extra": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			finalPublished := false
			arguments, err := json.Marshal(test.args)
			if err != nil {
				t.Fatal(err)
			}
			result, err := NewInsertAfter(cwd).Execute(context.Background(), arguments, toolUpdateSinkFunc(func(agent.ToolPresentation) error {
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
