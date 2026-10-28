package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

func TestReadRangesTextAndReportsContinuation(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "sample.txt"), "one\ntwo\nthree", 0o644)
	readTool := NewRead(cwd)

	result := executeJSON(t, readTool, map[string]any{"path": "sample.txt", "offset": 2, "limit": 2})
	if result.IsError || result.Output != "two\nthree" {
		t.Fatalf("read result = %+v", result)
	}

	result = executeJSON(t, readTool, map[string]any{"path": "sample.txt", "offset": 2, "limit": 1})
	if result.IsError || !strings.HasPrefix(result.Output, "two\n") {
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
	if result := executeJSON(t, readTool, map[string]any{"path": "late-binary.dat"}); result.IsError || !strings.Contains(result.Output, fmt.Sprintf("next offset: %d", defaultMaxLines)) {
		t.Fatalf("binary after result limit = %+v", result.Output[len(result.Output)-min(len(result.Output), 100):])
	}
	for _, test := range []struct {
		name          string
		args          map[string]any
		wantRequested string
	}{
		{name: "binary", args: map[string]any{"path": "binary.dat"}},
		{name: "empty offset", args: map[string]any{"path": "empty.txt", "offset": 2}},
		{name: "zero offset", args: map[string]any{"path": "target.txt", "offset": 0}},
		{name: "large limit", args: map[string]any{"path": "target.txt", "limit": defaultMaxLines + 1}, wantRequested: fmt.Sprint(defaultMaxLines + 1)},
		{name: "directory", args: map[string]any{"path": "."}},
		{name: "fifo", args: map[string]any{"path": "pipe"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := executeJSON(t, readTool, test.args)
			if !result.IsError {
				t.Fatalf("read succeeded: %+v", result)
			}
			if test.wantRequested != "" && !strings.Contains(result.Output, test.wantRequested) {
				t.Fatalf("read error omits requested value %q: %+v", test.wantRequested, result)
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

	for _, test := range []struct {
		name     string
		source   string
		retained string
	}{
		{name: "lines.txt", source: manyLines, retained: "line"},
		{name: "long.txt", source: longLine, retained: "é"},
	} {
		result := executeJSON(t, readTool, map[string]any{"path": test.name})
		if result.IsError {
			t.Fatalf("read %s = %+v", test.name, result)
		}
		if len(result.Output) > defaultMaxBytes || countLines(result.Output) > defaultMaxLines {
			t.Fatalf("read %s is not bounded: %d bytes, %d lines", test.name, len(result.Output), countLines(result.Output))
		}
		if !utf8.ValidString(result.Output) || result.Output == test.source || !strings.Contains(result.Output, test.retained) {
			t.Fatalf("read %s did not retain a bounded UTF-8 prefix: %q", test.name, result.Output[len(result.Output)-min(len(result.Output), 100):])
		}
	}
}

func TestReadChecksCancellationWhileScanning(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "large.txt"), strings.Repeat("x\n", 1_000), 0o644)
	readTool := NewRead(cwd)
	ctx := &cancelAfterChecksContext{cancelAfter: 100}

	result, err := readTool.Execute(ctx, json.RawMessage(`{"path":"large.txt","offset":5000}`), discardUpdates)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation error = %v", err)
	}
	if result != (agent.ToolResult{}) {
		t.Fatalf("read cancellation result = %+v", result)
	}
}

func TestScanReadRangeStopsAtWindow(t *testing.T) {
	lateBinary := strings.NewReader("selected\nignored\x00")
	if output, err := scanReadRange(context.Background(), lateBinary, 1, 1); err != nil || output != "selected\n[truncated; next offset: 2]\n" {
		t.Fatalf("late binary output = %q, error = %v", output, err)
	}
	if _, err := scanReadRange(context.Background(), strings.NewReader("selected\x00\n"), 1, 1); !errors.Is(err, errReadBinary) {
		t.Fatalf("selected binary error = %v", err)
	}

	readErr := errors.New("read failed")
	source := io.MultiReader(strings.NewReader("text"), failingReader{err: readErr})
	if _, err := scanReadRange(context.Background(), source, 1, 1); !errors.Is(err, readErr) {
		t.Fatalf("reader error = %v", err)
	}

	output, err := scanReadRange(context.Background(), strings.NewReader(strings.Repeat("é", defaultMaxBytes)), 1, 1)
	if err != nil || len(output) > defaultMaxBytes || !utf8.ValidString(output) || output == strings.Repeat("é", defaultMaxBytes) || !strings.Contains(output, "é") {
		t.Fatalf("long-line output bytes=%d valid=%t error=%v", len(output), utf8.ValidString(output), err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanReadRange(ctx, strings.NewReader("text"), 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestReadPresentationShowsRequestedLineRange(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "offset and limit", arguments: `{"path":"demo.go","offset":120,"limit":210}`, want: "demo.go:120-329"},
		{name: "offset", arguments: `{"path":"demo.go","offset":120}`, want: "demo.go:120"},
		{name: "limit", arguments: `{"path":"demo.go","limit":210}`, want: "demo.go:1-210"},
		{name: "default range", arguments: `{"path":"demo.go","offset":null,"limit":null}`, want: "demo.go"},
	}

	readTool := NewRead(t.TempDir())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation := readTool.Presentation(test.arguments)
			if presentation.Arguments != test.want {
				t.Fatalf("presentation = %+v, want arguments %q", presentation, test.want)
			}
		})
	}
}
