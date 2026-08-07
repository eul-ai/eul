package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"yaah/agent"
)

func TestCoreToolDefinitionsUseStrictSchemas(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	editTool := NewEdit(cwd)
	bashTool := NewBash(cwd)

	tests := []struct {
		tool       Tool
		required   []string
		properties map[string][]string
	}{
		{tool: readTool, required: []string{"path", "offset", "limit"}, properties: map[string][]string{"path": {"string"}, "offset": {"integer", "null"}, "limit": {"integer", "null"}}},
		{tool: writeTool, required: []string{"path", "content"}, properties: map[string][]string{"path": {"string"}, "content": {"string"}}},
		{tool: editTool, required: []string{"path", "oldText", "newText"}, properties: map[string][]string{"path": {"string"}, "oldText": {"string"}, "newText": {"string"}}},
		{tool: bashTool, required: []string{"command", "timeout"}, properties: map[string][]string{"command": {"string"}, "timeout": {"integer", "null"}}},
	}
	for _, test := range tests {
		definition := test.tool.Definition()
		t.Run(definition.Name, func(t *testing.T) {
			if definition.Parameters.Type != "object" {
				t.Fatalf("schema type = %q", definition.Parameters.Type)
			}
			if definition.Parameters.AdditionalProperties == nil || *definition.Parameters.AdditionalProperties {
				t.Fatal("schema does not reject additional properties")
			}
			if !slices.Equal(definition.Parameters.Required, test.required) {
				t.Fatalf("required fields = %v, want %v", definition.Parameters.Required, test.required)
			}
			if len(definition.Parameters.Properties) != len(test.properties) {
				t.Fatalf("properties = %v, want %v", definition.Parameters.Properties, test.properties)
			}
			for name, wantTypes := range test.properties {
				property, exists := definition.Parameters.Properties[name]
				if !exists {
					t.Fatalf("missing property %q", name)
				}
				gotTypes := []string{property.Type}
				if len(property.AnyOf) > 0 {
					gotTypes = gotTypes[:0]
					for _, item := range property.AnyOf {
						gotTypes = append(gotTypes, item.Type)
					}
				}
				if !slices.Equal(gotTypes, wantTypes) {
					t.Fatalf("property %q types = %v, want %v", name, gotTypes, wantTypes)
				}
			}
		})
	}
}

func TestReadRangesTextAndReportsContinuation(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "sample.txt"), "one\ntwo\nthree", 0o644)
	readTool := NewRead(cwd)

	result := executeJSON(t, readTool, map[string]any{"path": "sample.txt", "offset": 2, "limit": 2})
	if result.IsError || result.Output != "two\nthree" {
		t.Fatalf("read result = %+v", result)
	}

	result = executeJSON(t, readTool, map[string]any{"path": "sample.txt", "offset": 2, "limit": 1})
	if result.IsError || !strings.HasPrefix(result.Output, "two\n") || !strings.Contains(result.Output, "next offset: 3") {
		t.Fatalf("limited read result = %+v", result)
	}
}

func TestReadHandlesEmptyBinarySymlinkAndInvalidRanges(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "empty.txt"), "", 0o644)
	mustWriteFile(t, filepath.Join(cwd, "binary.dat"), "text\x00data", 0o644)
	mustWriteFile(t, filepath.Join(cwd, "late-binary.dat"), strings.Repeat("text\n", defaultMaxLines+1)+"\x00", 0o644)
	mustWriteFile(t, filepath.Join(cwd, "target.txt"), "target", 0o644)
	if err := syscall.Mkfifo(filepath.Join(cwd, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(cwd, "link.txt")); err != nil {
		t.Fatal(err)
	}
	readTool := NewRead(cwd)

	if result := executeJSON(t, readTool, map[string]any{"path": "empty.txt"}); result.IsError || result.Output != "" {
		t.Fatalf("empty read = %+v", result)
	}
	if result := executeJSON(t, readTool, map[string]any{"path": "link.txt"}); result.IsError || result.Output != "target" {
		t.Fatalf("symlink read = %+v", result)
	}
	for _, test := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "binary", args: map[string]any{"path": "binary.dat"}, want: "binary file"},
		{name: "binary after result limit", args: map[string]any{"path": "late-binary.dat"}, want: "binary file"},
		{name: "empty offset", args: map[string]any{"path": "empty.txt", "offset": 2}, want: "beyond end"},
		{name: "zero offset", args: map[string]any{"path": "target.txt", "offset": 0}, want: "must be positive"},
		{name: "large limit", args: map[string]any{"path": "target.txt", "limit": defaultMaxLines + 1}, want: "must not exceed"},
		{name: "directory", args: map[string]any{"path": "."}, want: "not a regular file"},
		{name: "fifo", args: map[string]any{"path": "pipe"}, want: "not a regular file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := executeJSON(t, readTool, test.args)
			if !result.IsError || !strings.Contains(result.Output, test.want) {
				t.Fatalf("read error = %+v, want %q", result, test.want)
			}
		})
	}
}

func TestReadOutputIsBoundedAndUTF8(t *testing.T) {
	cwd := t.TempDir()
	manyLines := strings.Repeat("line\n", defaultMaxLines+10)
	mustWriteFile(t, filepath.Join(cwd, "lines.txt"), manyLines, 0o644)
	longLine := strings.Repeat("é", defaultMaxBytes)
	mustWriteFile(t, filepath.Join(cwd, "long.txt"), longLine, 0o644)
	readTool := NewRead(cwd)

	for _, name := range []string{"lines.txt", "long.txt"} {
		result := executeJSON(t, readTool, map[string]any{"path": name})
		if result.IsError {
			t.Fatalf("read %s = %+v", name, result)
		}
		if len(result.Output) > defaultMaxBytes || countLines(result.Output) > defaultMaxLines {
			t.Fatalf("read %s is not bounded: %d bytes, %d lines", name, len(result.Output), countLines(result.Output))
		}
		if !utf8.ValidString(result.Output) || !strings.Contains(result.Output, "truncated") {
			t.Fatalf("read %s missing valid truncation output: %q", name, result.Output[len(result.Output)-min(len(result.Output), 100):])
		}
	}
}

func TestReadChecksCancellationWhileScanning(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "large.txt"), strings.Repeat("x", defaultMaxBytes*4), 0o644)
	readTool := NewRead(cwd)
	ctx := &cancelAfterChecksContext{cancelAfter: 100}

	result, err := readTool.Execute(ctx, json.RawMessage(`{"path":"large.txt","offset":2}`), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation error = %v", err)
	}
	if result != (agent.ToolResult{}) {
		t.Fatalf("read cancellation result = %+v", result)
	}
}

func TestScanReadRangeValidatesTheFullStream(t *testing.T) {
	lateBinary := strings.NewReader("selected\nignored\x00")
	if _, err := scanReadRange(context.Background(), lateBinary, 1, 1); !errors.Is(err, errReadBinary) {
		t.Fatalf("late binary error = %v", err)
	}

	readErr := errors.New("read failed")
	source := io.MultiReader(strings.NewReader("text"), failingReader{err: readErr})
	if _, err := scanReadRange(context.Background(), source, 1, 1); !errors.Is(err, readErr) {
		t.Fatalf("reader error = %v", err)
	}

	output, err := scanReadRange(context.Background(), strings.NewReader(strings.Repeat("é", defaultMaxBytes)), 1, 1)
	if err != nil || len(output) > defaultMaxBytes || !utf8.ValidString(output) || !strings.Contains(output, "truncated") {
		t.Fatalf("long-line output bytes=%d valid=%t error=%v", len(output), utf8.ValidString(output), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanReadRange(ctx, strings.NewReader("text"), 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestWriteCreatesParentsOverwritesAndPreservesMode(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)

	result := executeJSON(t, writeTool, map[string]any{"path": "nested/file.txt", "content": "first"})
	if result.IsError || !strings.Contains(result.Output, "wrote 5 bytes") {
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

func TestFileToolPresentationsSeparateTitleAndArguments(t *testing.T) {
	snapshot := agent.ToolCallSnapshot{Arguments: map[string]any{"path": "demo.go"}}
	presentations := []agent.ToolPresentation{
		NewRead(t.TempDir()).Presentation(snapshot),
		NewWrite(t.TempDir()).Presentation(snapshot),
		NewEdit(t.TempDir()).Presentation(snapshot),
		bashPresentation("go test ./..."),
	}
	wantTitles := []string{"read", "write", "edit", "bash"}
	wantArguments := []string{"demo.go", "demo.go", "demo.go", `"go test ./..."`}
	for index, presentation := range presentations {
		if presentation.Title != wantTitles[index] || presentation.Arguments != wantArguments[index] {
			t.Fatalf("presentation %d = %+v", index, presentation)
		}
	}
}

func TestWritePresentationStreamsBoundedPreviewWithoutWriting(t *testing.T) {
	cwd := t.TempDir()
	writeTool := NewWrite(cwd)
	content := strings.Join([]string{
		"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
	}, "\n")
	raw := `{"path":"preview.txt","content":"` + strings.ReplaceAll(content, "\n", `\n`)
	snapshot := agent.ToolCallSnapshot{
		ID: "write-1", Name: "write", RawArguments: raw,
		Arguments: map[string]any{"path": "preview.txt", "content": content},
	}
	presentation := writeTool.Presentation(snapshot)
	if presentation.Title != "write" || presentation.Arguments != "preview.txt" || len(presentation.Lines) != 11 || presentation.Lines[0] != "one" || presentation.Lines[9] != "ten" || presentation.Lines[10] != "… (2 more lines, 12 total)" {
		t.Fatalf("presentation = %+v", presentation)
	}
	if _, err := os.Stat(filepath.Join(cwd, "preview.txt")); !os.IsNotExist(err) {
		t.Fatalf("presentation changed filesystem: %v", err)
	}

	huge := strings.Repeat("界", writePresentationMaxBytes)
	hugeSnapshot := agent.ToolCallSnapshot{Arguments: map[string]any{"path": "huge.txt", "content": huge}}
	hugePresentation := writeTool.Presentation(hugeSnapshot)
	if len(strings.Join(hugePresentation.Lines, "\n")) > writePresentationMaxBytes || !strings.Contains(hugePresentation.Lines[len(hugePresentation.Lines)-1], "truncated") {
		t.Fatalf("huge presentation bytes=%d lines=%+v", len(strings.Join(hugePresentation.Lines, "\n")), hugePresentation.Lines)
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
	if !result.IsError || !strings.Contains(result.Output, "file may have changed") {
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

func TestEditReplacesUniqueTextAndPreservesMode(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sample.txt")
	mustWriteFile(t, path, "before old after", 0o600)
	editTool := NewEdit(cwd)

	result := executeJSON(t, editTool, map[string]any{"path": "sample.txt", "oldText": "old", "newText": "new"})
	if result.IsError || mustReadFile(t, path) != "before new after" {
		t.Fatalf("edit = %+v, content = %q", result, mustReadFile(t, path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after edit = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yaah-edit-") {
			t.Fatalf("temporary edit file was not cleaned up: %s", entry.Name())
		}
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
		want string
	}{
		{name: "zero", args: map[string]any{"path": "sample.txt", "oldText": "missing", "newText": "new"}, want: "not found"},
		{name: "multiple", args: map[string]any{"path": "sample.txt", "oldText": "same", "newText": "new"}, want: "2 times"},
		{name: "empty old", args: map[string]any{"path": "sample.txt", "oldText": "", "newText": "new"}, want: "nonempty"},
		{name: "missing new", args: map[string]any{"path": "sample.txt", "oldText": "same"}, want: "newText"},
		{name: "unknown field", args: map[string]any{"path": "sample.txt", "oldText": "same", "newText": "new", "extra": true}, want: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeInfo, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			result := executeJSON(t, editTool, test.args)
			if !result.IsError || !strings.Contains(result.Output, test.want) {
				t.Fatalf("edit failure = %+v, want %q", result, test.want)
			}
			afterInfo, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if mustReadFile(t, path) != original || afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
				t.Fatalf("failed edit modified file: content=%q mode=%o", mustReadFile(t, path), afterInfo.Mode().Perm())
			}
		})
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
	if result := executeJSON(t, editTool, map[string]any{"path": "link.txt", "oldText": "old", "newText": "new"}); result.IsError {
		t.Fatalf("symlink edit = %+v", result)
	}
	after, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode()&os.ModeSymlink == 0 || after.Mode()&os.ModeSymlink == 0 || mustReadFile(t, target) != "new" {
		t.Fatalf("edit replaced symlink or missed target: before=%v after=%v target=%q", before.Mode(), after.Mode(), mustReadFile(t, target))
	}

	beforeInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if result := executeJSON(t, editTool, map[string]any{"path": "target.txt", "oldText": "new", "newText": "new"}); result.IsError || !strings.Contains(result.Output, "no changes") {
		t.Fatalf("no-op edit = %+v", result)
	}
	afterInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("no-op edit rewrote the file")
	}
	if result := executeJSON(t, editTool, map[string]any{"path": "binary.dat", "oldText": "old", "newText": "new"}); !result.IsError || !strings.Contains(result.Output, "binary file") {
		t.Fatalf("binary edit = %+v", result)
	}
}

func TestFilesystemToolsHonorPreCanceledContext(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	editTool := NewEdit(cwd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := []struct {
		tool Tool
		args string
	}{
		{readTool, `{"path":"missing"}`},
		{writeTool, `{"path":"file","content":"content"}`},
		{editTool, `{"path":"file","oldText":"old","newText":"new"}`},
	}
	for _, call := range calls {
		_, err := call.tool.Execute(ctx, json.RawMessage(call.args), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation error = %v", call.tool.Definition().Name, err)
		}
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Fatalf("canceled filesystem tools changed cwd: entries=%v err=%v", entries, err)
	}
}

func TestCoreToolsRegisterInDeterministicOrder(t *testing.T) {
	cwd := t.TempDir()
	readTool := NewRead(cwd)
	writeTool := NewWrite(cwd)
	editTool := NewEdit(cwd)
	bashTool := NewBash(cwd)

	registry := NewRegistry(readTool, writeTool, editTool, bashTool)
	definitions := registry.Definitions()
	names := make([]string, len(definitions))
	for i, definition := range definitions {
		names[i] = definition.Name
	}
	if !slices.Equal(names, []string{"bash", "edit", "read", "write"}) {
		t.Fatalf("definition names = %v", names)
	}
}

func executeJSON(t *testing.T, current Tool, arguments any) agent.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}

	result, err := current.Execute(context.Background(), encoded, nil)
	if err != nil {
		t.Fatalf("%s.Execute() error = %v", current.Definition().Name, err)
	}
	return result
}

type cancelAfterChecksContext struct {
	checks      int
	cancelAfter int
}

func (c *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (c *cancelAfterChecksContext) Value(any) any               { return nil }
func (c *cancelAfterChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}
