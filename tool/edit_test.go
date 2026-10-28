package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

func TestEditReplacesUniqueTextAndPreservesMode(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	mustWriteFile(t, path, "before old after", 0o600)
	editTool := NewEdit(cwd)

	result := executeJSON(t, editTool, map[string]any{"path": "sample.txt", "old_text": "old", "new_text": "new", "all": false})
	if result.IsError || mustReadFile(t, path) != "before new after" {
		t.Fatalf("replace = %+v, content = %q", result, mustReadFile(t, path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after replace = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".eul-replace-") {
			t.Fatalf("temporary replace file was not cleaned up: %s", entry.Name())
		}
	}
}

func TestEditDefaultsToSingleAndCanReplaceAll(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	mustWriteFile(t, path, "same same", 0o600)
	editTool := NewEdit(cwd)

	result := executeJSON(t, editTool, map[string]any{"path": "sample.txt", "old_text": "same", "new_text": "new"})
	if !result.IsError || mustReadFile(t, path) != "same same" {
		t.Fatalf("default replace = %+v, content = %q", result, mustReadFile(t, path))
	}

	result = executeJSON(t, editTool, map[string]any{"path": "sample.txt", "old_text": "same", "new_text": "new", "all": true})
	if result.IsError || mustReadFile(t, path) != "new new" {
		t.Fatalf("replace all = %+v, content = %q", result, mustReadFile(t, path))
	}
}

func TestParallelEditsToSameFileAllApply(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	words := []string{"alpha", "beta", "gamma", "delta"}
	mustWriteFile(t, path, strings.Join(words, " "), 0o644)
	editTool := NewEdit(cwd)

	type outcome struct {
		result agent.ToolResult
		err    error
	}
	var edits sync.WaitGroup
	outcomes := make(chan outcome, len(words))
	for _, word := range words {
		arguments := fmt.Sprintf(`{"path":"sample.txt","old_text":%q,"new_text":%q,"all":false}`, word, strings.ToUpper(word))
		edits.Go(func() {
			result, err := editTool.Execute(context.Background(), json.RawMessage(arguments), discardUpdates)
			outcomes <- outcome{result: result, err: err}
		})
	}
	edits.Wait()
	close(outcomes)

	for outcome := range outcomes {
		if outcome.err != nil || outcome.result.IsError {
			t.Fatalf("parallel edit = %+v, error = %v", outcome.result, outcome.err)
		}
	}
	if got := mustReadFile(t, path); got != "ALPHA BETA GAMMA DELTA" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditPublishesDiffAfterSuccessfulCommit(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	original := strings.Join([]string{"zero", "one", "two", "three", "four", "old", "six", "seven", "eight", "nine", "ten"}, "\n")
	replacement := strings.Replace(original, "old", "new", 1)
	mustWriteFile(t, path, original, 0o600)
	editTool := NewEdit(cwd)

	var presentation agent.ToolPresentation
	result, err := editTool.Execute(
		context.Background(),
		json.RawMessage(`{"path":"sample.txt","old_text":"old","new_text":"new","all":false}`),
		toolUpdateSinkFunc(func(update agent.ToolPresentation) error {
			if got := mustReadFile(t, path); got != replacement {
				t.Fatalf("diff published before commit: content = %q", got)
			}
			presentation = update
			return nil
		}),
	)
	if err != nil || result.IsError {
		t.Fatalf("replace result = %+v, error = %v", result, err)
	}

	wantDiff := []agent.ToolDiffLine{
		{Kind: agent.ToolDiffLineContext, OldLine: 2, NewLine: 2, Text: "one"},
		{Kind: agent.ToolDiffLineContext, OldLine: 3, NewLine: 3, Text: "two"},
		{Kind: agent.ToolDiffLineContext, OldLine: 4, NewLine: 4, Text: "three"},
		{Kind: agent.ToolDiffLineContext, OldLine: 5, NewLine: 5, Text: "four"},
		{Kind: agent.ToolDiffLineRemoved, OldLine: 6, Text: "old"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 6, Text: "new"},
		{Kind: agent.ToolDiffLineContext, OldLine: 7, NewLine: 7, Text: "six"},
		{Kind: agent.ToolDiffLineContext, OldLine: 8, NewLine: 8, Text: "seven"},
		{Kind: agent.ToolDiffLineContext, OldLine: 9, NewLine: 9, Text: "eight"},
		{Kind: agent.ToolDiffLineContext, OldLine: 10, NewLine: 10, Text: "nine"},
	}
	if presentation.Title != editToolName || presentation.Arguments != "sample.txt" || !slices.Equal(presentation.Diff, wantDiff) {
		t.Fatalf("replace presentation = %+v, want diff %+v", presentation, wantDiff)
	}
}

func editDiff(original, oldText, newText string) []agent.ToolDiffLine {
	starts := matchStarts([]byte(original), []byte(oldText))
	replacement := strings.ReplaceAll(original, oldText, newText)
	return buildFileDiff([]byte(original), []byte(replacement), starts, len(oldText), len(newText))
}

func TestBuildFileDiffTracksOldAndNewLineNumbers(t *testing.T) {
	diff := editDiff("a\r\nb\r\nc\r\n", "b", "x\r\ny")
	want := []agent.ToolDiffLine{
		{Kind: agent.ToolDiffLineContext, OldLine: 1, NewLine: 1, Text: "a"},
		{Kind: agent.ToolDiffLineRemoved, OldLine: 2, Text: "b"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 2, Text: "x"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 3, Text: "y"},
		{Kind: agent.ToolDiffLineContext, OldLine: 3, NewLine: 4, Text: "c"},
	}
	if !slices.Equal(diff, want) {
		t.Fatalf("diff = %+v, want %+v", diff, want)
	}
}

func TestBuildFileDiffShowsLinesJoinedByReplacement(t *testing.T) {
	diff := editDiff("x\ny\nz\n", "x\n", "w")
	want := []agent.ToolDiffLine{
		{Kind: agent.ToolDiffLineRemoved, OldLine: 1, Text: "x"},
		{Kind: agent.ToolDiffLineRemoved, OldLine: 2, Text: "y"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "wy"},
		{Kind: agent.ToolDiffLineContext, OldLine: 3, NewLine: 2, Text: "z"},
	}
	if !slices.Equal(diff, want) {
		t.Fatalf("diff = %+v, want %+v", diff, want)
	}
}

func TestBuildFileDiffMergesNearbyReplacements(t *testing.T) {
	diff := editDiff("a\nkeep\na", "a", "x")
	want := []agent.ToolDiffLine{
		{Kind: agent.ToolDiffLineRemoved, OldLine: 1, Text: "a"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 1, Text: "x"},
		{Kind: agent.ToolDiffLineContext, OldLine: 2, NewLine: 2, Text: "keep"},
		{Kind: agent.ToolDiffLineRemoved, OldLine: 3, Text: "a"},
		{Kind: agent.ToolDiffLineAdded, NewLine: 3, Text: "x"},
	}
	if !slices.Equal(diff, want) {
		t.Fatalf("diff = %+v, want %+v", diff, want)
	}
}

func TestBuildFileDiffSeparatesDistantHunks(t *testing.T) {
	context := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10"}
	original := strings.Join(append(append([]string{"old one"}, context...), "old two"), "\n")
	diff := editDiff(original, "old", "new")

	wantTexts := []string{"old one", "new one", "c1", "c2", "c3", "c4", fileDiffHunkMarker, "c7", "c8", "c9", "c10", "old two", "new two"}
	gotTexts := make([]string, len(diff))
	for index, line := range diff {
		gotTexts[index] = line.Text
	}
	if !slices.Equal(gotTexts, wantTexts) || diff[6].Kind != agent.ToolDiffLineOmitted || diff[11].OldLine != 12 || diff[12].NewLine != 12 {
		t.Fatalf("diff = %+v, want texts %q and final line 12", diff, wantTexts)
	}
}

func TestBuildFileDiffBoundsPresentation(t *testing.T) {
	for _, test := range []struct {
		name     string
		original string
		oldText  string
		newText  string
	}{
		{name: "lines", original: strings.Repeat("old\n", defaultMaxLines+10), oldText: "old", newText: "new"},
		{name: "bytes", original: strings.Repeat("界", defaultMaxBytes), oldText: "界", newText: "新"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diff := editDiff(test.original, test.oldText, test.newText)
			if len(diff) > defaultMaxLines || diff[len(diff)-1].Kind != agent.ToolDiffLineOmitted {
				t.Fatalf("bounded diff lines = %d, last = %+v", len(diff), diff[len(diff)-1])
			}
			bytes := 0
			for _, line := range diff {
				bytes += len(line.Text)
				if !utf8.ValidString(line.Text) {
					t.Fatalf("invalid UTF-8 diff line: %q", line.Text)
				}
			}
			if bytes > defaultMaxBytes {
				t.Fatalf("bounded diff bytes = %d", bytes)
			}
		})
	}
}

func TestFailedEditsNeverModifyFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	original := "same same"
	mustWriteFile(t, path, original, 0o640)
	editTool := NewEdit(cwd)

	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: "zero", args: map[string]any{"path": "sample.txt", "old_text": "missing", "new_text": "new", "all": false}},
		{name: "multiple", args: map[string]any{"path": "sample.txt", "old_text": "same", "new_text": "new"}},
		{name: "empty old", args: map[string]any{"path": "sample.txt", "old_text": "", "new_text": "new", "all": false}},
		{name: "binary replacement", args: map[string]any{"path": "sample.txt", "old_text": original, "new_text": "new\x00", "all": false}},
		{name: "missing new", args: map[string]any{"path": "sample.txt", "old_text": "same", "all": false}},
		{name: "unknown field", args: map[string]any{"path": "sample.txt", "old_text": "same", "new_text": "new", "all": false, "extra": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeInfo, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			arguments, marshalErr := json.Marshal(test.args)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			finalPublished := false
			result, executeErr := editTool.Execute(context.Background(), arguments, toolUpdateSinkFunc(func(agent.ToolPresentation) error {
				finalPublished = true
				return nil
			}))
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			if !result.IsError || finalPublished {
				t.Fatalf("replace failure = %+v, final published=%t", result, finalPublished)
			}
			afterInfo, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if mustReadFile(t, path) != original || afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
				t.Fatalf("failed replace modified file: content=%q mode=%o", mustReadFile(t, path), afterInfo.Mode().Perm())
			}
		})
	}
	matches, err := filepath.Glob(filepath.Join(cwd, ".eul-replace-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files=%v error=%v", matches, err)
	}
}

func TestEditNoOpBinaryAndSymlink(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "target.txt")
	mustWriteFile(t, target, "old", 0o644)
	mustWriteFile(t, filepath.Join(cwd, "binary.dat"), "old\x00", 0o644)
	link := filepath.Join(cwd, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatal(err)
	}
	editTool := NewEdit(cwd)

	before, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if result := executeJSON(t, editTool, map[string]any{"path": "link.txt", "old_text": "old", "new_text": "new", "all": false}); result.IsError {
		t.Fatalf("symlink replace = %+v", result)
	}
	after, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode()&os.ModeSymlink == 0 || after.Mode()&os.ModeSymlink == 0 || mustReadFile(t, target) != "new" {
		t.Fatalf("replace replaced symlink or missed target: before=%v after=%v target=%q", before.Mode(), after.Mode(), mustReadFile(t, target))
	}

	beforeInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	finalPublished := false
	result, err := editTool.Execute(
		context.Background(),
		json.RawMessage(`{"path":"target.txt","old_text":"new","new_text":"new","all":false}`),
		toolUpdateSinkFunc(func(agent.ToolPresentation) error {
			finalPublished = true
			return nil
		}),
	)
	if err != nil || result.IsError || finalPublished {
		t.Fatalf("no-op replace = %+v, final published=%t, error=%v", result, finalPublished, err)
	}
	afterInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("no-op replace rewrote the file")
	}
	if result := executeJSON(t, editTool, map[string]any{"path": "binary.dat", "old_text": "old", "new_text": "new", "all": false}); !result.IsError {
		t.Fatalf("binary replace = %+v", result)
	}
}
