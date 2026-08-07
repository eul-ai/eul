package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"yaah/agent"
)

func TestBashReportsCombinedOutputStatusCWDAndEnvironment(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("YAAH_TEST", "value")
	bashTool := NewBash(cwd)
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

func TestBashFinalPresentationShowsOutputTailAndDuration(t *testing.T) {
	bashTool := NewBash(t.TempDir())
	arguments, err := json.Marshal(map[string]any{"command": `printf 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n'`})
	if err != nil {
		t.Fatal(err)
	}
	updates := &recordingBashUpdates{}
	result, err := bashTool.Execute(context.Background(), arguments, updates)
	if err != nil || result.IsError {
		t.Fatalf("result = %+v, err = %v", result, err)
	}

	presentation, finalCalls := updates.finalPresentation()
	if finalCalls != 1 {
		t.Fatalf("SetFinal calls = %d, want 1", finalCalls)
	}
	wantLines := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	if !slices.Equal(presentation.Lines, wantLines) || presentation.TailLines != bashPreviewLines || presentation.Elapsed <= 0 {
		t.Fatalf("presentation = %+v", presentation)
	}
	if presentation.Outcome != "exit status: 0" || !strings.Contains(result.Output, "one") || !strings.Contains(result.Output, "eight") {
		t.Fatalf("presentation = %+v, result = %+v", presentation, result)
	}
}

func TestBashStreamsOutputBeforeCommandCompletes(t *testing.T) {
	bashTool := NewBash(t.TempDir())
	arguments := json.RawMessage(`{"command":"printf 'first\\n'; sleep 0.4; printf 'second\\n'"}`)
	updates := make(chan agent.ToolPresentation, 16)
	type outcome struct {
		result agent.ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := bashTool.Execute(context.Background(), arguments, toolUpdateSinkFunc(func(update agent.ToolPresentation) error {
			updates <- update
			return nil
		}))
		done <- outcome{result: result, err: err}
	}()

	select {
	case update := <-updates:
		if !slices.Equal(update.Lines, []string{"first"}) || update.Outcome != "" || update.Elapsed <= 0 {
			t.Fatalf("streamed presentation = %+v", update)
		}
	case result := <-done:
		t.Fatalf("command completed before streaming output: %+v", result)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streamed bash output")
	}

	select {
	case completed := <-done:
		if completed.err != nil || completed.result.IsError || !strings.Contains(completed.result.Output, "second") {
			t.Fatalf("completed result = %+v, err = %v", completed.result, completed.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bash completion")
	}
}

func TestBashUpdateFailureCancelsCommandAndReturnsError(t *testing.T) {
	bashTool := NewBash(t.TempDir())
	bashTool.defaultTimeout = 5 * time.Second
	bashTool.maxTimeout = 5 * time.Second
	updateErr := errors.New("update failed")
	started := time.Now()
	result, err := bashTool.Execute(
		context.Background(),
		json.RawMessage(`{"command":"printf started; while :; do :; done"}`),
		toolUpdateSinkFunc(func(agent.ToolPresentation) error { return updateErr }),
	)
	if !errors.Is(err, updateErr) {
		t.Fatalf("error = %v, want %v", err, updateErr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("update failure took %s", elapsed)
	}
	if !result.IsError || !strings.Contains(result.Output, "started") {
		t.Fatalf("result = %+v", result)
	}
}

func TestBashSupportsMVPDiscoveryCommands(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd)
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
	bashTool := NewBash(cwd)

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
	bashTool := NewBash(cwd)
	bashTool.defaultTimeout = 30 * time.Millisecond
	bashTool.maxTimeout = time.Second
	bashTool.waitDelay = 20 * time.Millisecond

	arguments, err := json.Marshal(map[string]any{"command": `printf 'before-timeout\n'; while :; do :; done`})
	if err != nil {
		t.Fatal(err)
	}
	updates := &recordingBashUpdates{}
	started := time.Now()
	result, err := bashTool.Execute(context.Background(), arguments, updates)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !result.IsError || !strings.Contains(result.Output, "before-timeout") || !strings.Contains(result.Output, "timed out after 30ms") || !strings.Contains(result.Output, "exit status:") {
		t.Fatalf("timeout result = %+v", result)
	}
	presentation, finalCalls := updates.finalPresentation()
	if finalCalls != 1 || !slices.Equal(presentation.Lines, []string{"before-timeout"}) || !strings.Contains(presentation.Outcome, "timed out after 30ms") || presentation.Elapsed <= 0 {
		t.Fatalf("timeout presentation = %+v, final calls = %d", presentation, finalCalls)
	}
}

func TestBashParentCancellationIsFatalAndReported(t *testing.T) {
	cwd := t.TempDir()
	readyPath := filepath.Join(cwd, "ready")
	t.Setenv("YAAH_READY", readyPath)
	bashTool := NewBash(cwd)
	bashTool.defaultTimeout = 5 * time.Second
	bashTool.maxTimeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result agent.ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	updates := &recordingBashUpdates{}
	go func() {
		result, runErr := bashTool.Execute(ctx, json.RawMessage(`{"command":": > \"$YAAH_READY\"; printf ready; while :; do :; done"}`), updates)
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
		presentation, finalCalls := updates.finalPresentation()
		if finalCalls != 1 || !slices.Equal(presentation.Lines, []string{"ready"}) || presentation.Outcome != "exit status: -1; canceled" || presentation.Elapsed <= 0 {
			t.Fatalf("cancellation presentation = %+v, final calls = %d", presentation, finalCalls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bash did not stop after parent cancellation")
	}
}

func TestBashOutputKeepsBoundedTail(t *testing.T) {
	cwd := t.TempDir()
	bashTool := NewBash(cwd)
	command := `printf 'START-MARKER\n'; i=0; while [ "$i" -lt 7000 ]; do printf 'line-%04d-xxxxxxxx\n' "$i"; i=$((i+1)); done; printf 'END-MARKER\n'`

	result := executeJSON(t, bashTool, map[string]any{"command": command})
	if result.IsError {
		t.Fatalf("bash result = %+v", result)
	}
	if len(result.Output) > defaultMaxBytes || countLines(result.Output) > defaultMaxLines {
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
	bashTool := NewBash(cwd)
	bashTool.shell = filepath.Join(cwd, "missing-bash")

	updates := &recordingBashUpdates{}
	result, err := bashTool.Execute(context.Background(), json.RawMessage(`{"command":"echo no"}`), updates)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "failed to start shell") || !strings.Contains(result.Output, "exit status: unavailable") {
		t.Fatalf("start failure = %+v, err = %v", result, err)
	}
	presentation, finalCalls := updates.finalPresentation()
	if finalCalls != 1 || presentation.Elapsed <= 0 {
		t.Fatalf("start failure presentation = %+v, final calls = %d", presentation, finalCalls)
	}

	regular := NewBash(cwd)
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
			result, runErr := regular.Execute(context.Background(), json.RawMessage(test.json), nil)
			if runErr != nil || !result.IsError || !strings.Contains(result.Output, test.want) {
				t.Fatalf("validation result = %+v err=%v, want %q", result, runErr, test.want)
			}
		})
	}

	largeField := strings.Repeat("x", defaultMaxBytes*2)
	encoded := fmt.Sprintf(`{"command":"echo no",%q:true}`, largeField)
	result, err = regular.Execute(context.Background(), json.RawMessage(encoded), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Output) > defaultMaxBytes || countLines(result.Output) > defaultMaxLines {
		t.Fatalf("large validation error is not bounded: %+v", result)
	}
}

type recordingBashUpdates struct {
	mu         sync.Mutex
	final      agent.ToolPresentation
	finalCalls int
}

func (*recordingBashUpdates) Update(agent.ToolPresentation) error {
	return nil
}

func (updates *recordingBashUpdates) SetFinal(presentation agent.ToolPresentation) {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	updates.final = presentation
	updates.finalCalls++
}

func (updates *recordingBashUpdates) finalPresentation() (agent.ToolPresentation, int) {
	updates.mu.Lock()
	defer updates.mu.Unlock()
	return updates.final, updates.finalCalls
}

func waitForPath(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
