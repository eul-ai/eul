package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yaah/agent"
)

func TestBashReportsCombinedOutputStatusCWDAndEnvironment(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{Env: []string{"YAAH_TEST=value"}})
	command := `printf 'stdout:%s:%s\n' "$PWD" "$YAAH_TEST"; printf 'stderr\n' >&2`
	result := executeJSON(t, bashTool, map[string]any{"command": command})
	if result.IsError {
		t.Fatalf("bash result = %+v", result)
	}
	for _, want := range []string{"stdout:" + cwd + ":value", "stderr", "[exit status: 0]"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("bash output = %q, want %q", result.Output, want)
		}
	}
}

func TestBashSupportsMVPDiscoveryCommands(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{})
	command := `mkdir -p nested; printf 'TODO\n' > nested/note.txt; grep -R TODO .; find . -name '*.txt'; ls nested`
	result := executeJSON(t, bashTool, map[string]any{"command": command})
	if result.IsError {
		t.Fatalf("discovery command result = %+v", result)
	}
	for _, want := range []string{"TODO", "./nested/note.txt", "note.txt", "[exit status: 0]"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("discovery output = %q, want %q", result.Output, want)
		}
	}
}

func TestBashReportsNonzeroExitAndNilStdin(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{})

	result := executeJSON(t, bashTool, map[string]any{"command": `printf 'failure'; exit 7`})
	if !result.IsError || !strings.Contains(result.Output, "failure") || !strings.Contains(result.Output, "[exit status: 7]") {
		t.Fatalf("nonzero result = %+v", result)
	}
	result = executeJSON(t, bashTool, map[string]any{"command": `if read value; then printf 'input:%s' "$value"; else printf 'eof'; fi`})
	if result.IsError || !strings.Contains(result.Output, "eof") || !strings.Contains(result.Output, "[exit status: 0]") {
		t.Fatalf("stdin result = %+v", result)
	}
}

func TestBashTimeoutIsRecoverableAndRetainsOutput(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{})
	bashTool.defaultTimeout = 30 * time.Millisecond
	bashTool.maxTimeout = time.Second
	bashTool.waitDelay = 20 * time.Millisecond

	started := time.Now()
	result := executeJSON(t, bashTool, map[string]any{"command": `printf 'before-timeout\n'; while :; do :; done`})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !result.IsError || !strings.Contains(result.Output, "before-timeout") || !strings.Contains(result.Output, "timed out after 30ms") || !strings.Contains(result.Output, "exit status:") {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestBashParentCancellationIsFatalAndReported(t *testing.T) {
	cwd := t.TempDir()
	readyPath := filepath.Join(cwd, "ready")
	environment := append(os.Environ(), "YAAH_READY="+readyPath)
	bashTool := NewBash(cwd, BashOptions{Env: environment})
	bashTool.defaultTimeout = 5 * time.Second
	bashTool.maxTimeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result agent.ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := bashTool.Execute(ctx, json.RawMessage(`{"command":": > \"$YAAH_READY\"; printf ready; while :; do :; done"}`))
		done <- outcome{result: result, err: runErr}
	}()
	waitForPath(t, readyPath)
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancellation error = %v", got.err)
		}
		if !got.result.IsError || !strings.Contains(got.result.Output, "canceled") || !strings.Contains(got.result.Output, "exit status:") {
			t.Fatalf("cancellation result = %+v", got.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bash did not stop after parent cancellation")
	}
}

func TestBashOutputKeepsBoundedTail(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{})
	command := `printf 'START-MARKER\n'; i=0; while [ "$i" -lt 7000 ]; do printf 'line-%04d-xxxxxxxx\n' "$i"; i=$((i+1)); done; printf 'END-MARKER\n'`
	result := executeJSON(t, bashTool, map[string]any{"command": command})
	if result.IsError {
		t.Fatalf("bash result = %+v", result)
	}
	if len(result.Output) > DefaultMaxBytes || countLines(result.Output) > DefaultMaxLines {
		t.Fatalf("bash output is not bounded: %d bytes, %d lines", len(result.Output), countLines(result.Output))
	}
	if !utf8.ValidString(result.Output) || !strings.Contains(result.Output, "earlier command output truncated") || !strings.Contains(result.Output, "END-MARKER") || !strings.Contains(result.Output, "[exit status: 0]") {
		t.Fatalf("bounded tail missing metadata/end: prefix=%q suffix=%q", result.Output[:min(len(result.Output), 100)], result.Output[len(result.Output)-min(len(result.Output), 100):])
	}
	if strings.Contains(result.Output, "START-MARKER") {
		t.Fatal("bounded tail retained the beginning of oversized output")
	}
}

func TestBashValidationAndStartFailuresAreRecoverableAndBounded(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd, BashOptions{})
	bashTool.shell = filepath.Join(cwd, "missing-bash")

	result := executeJSON(t, bashTool, map[string]any{"command": "echo no"})
	if !result.IsError || !strings.Contains(result.Output, "failed to start shell") || !strings.Contains(result.Output, "exit status: unavailable") {
		t.Fatalf("start failure = %+v", result)
	}

	regular := NewBash(cwd, BashOptions{})
	for _, test := range []struct {
		name string
		json string
		want string
	}{
		{name: "empty command", json: `{"command":""}`, want: "nonempty"},
		{name: "zero timeout", json: `{"command":"echo no","timeout":0}`, want: "positive"},
		{name: "unknown", json: `{"command":"echo no","extra":true}`, want: "unknown field"},
		{name: "wrong type", json: `{"command":3}`, want: "decode arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, runErr := regular.Execute(context.Background(), json.RawMessage(test.json))
			if runErr != nil || !result.IsError || !strings.Contains(result.Output, test.want) {
				t.Fatalf("validation result = %+v err=%v, want %q", result, runErr, test.want)
			}
		})
	}

	largeField := strings.Repeat("x", DefaultMaxBytes*2)
	encoded := fmt.Sprintf(`{"command":"echo no",%q:true}`, largeField)
	result, err := regular.Execute(context.Background(), json.RawMessage(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Output) > DefaultMaxBytes || countLines(result.Output) > DefaultMaxLines {
		t.Fatalf("large validation error is not bounded: %+v", result)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
