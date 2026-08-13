package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func TestNewLSPIsUnavailableWhenServerIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cwd := t.TempDir()
	writeLSPTestConfig(t, cwd)

	service, err := New(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if service.Available() {
		t.Fatal("service is available")
	}
}

func TestLSPServiceCloseStopsSessionsOnce(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	cwd := t.TempDir()
	writeLSPTestConfig(t, cwd)

	service, err := New(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !service.Available() {
		t.Fatal("service is unavailable")
	}
	stops := 0
	service.client.sessions["test"] = &lspSession{stopSession: func() { stops++ }}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("session stops = %d, want 1", stops)
	}
}

func TestLSPToolsWithGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}

	cwd := t.TempDir()
	writeLSPTestConfig(t, cwd)
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module example.com/sample\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const source = `package sample

type Thing struct {
	Value int
}

func Use(value Thing) int {
	return value.Value
}
`
	const testSource = `package sample

var testThing = Thing{Value: 1}
`
	path := filepath.Join(cwd, "sample.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(cwd, "sample_test.go")
	if err := os.WriteFile(testPath, []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}

	service, err := New(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	diagnostics := executeLSPTestOperation(t, ctx, service, "diagnostics", map[string]any{"path": "sample.go"})
	if !strings.Contains(diagnostics.Output, `"items": []`) {
		t.Fatalf("diagnostics = %s", diagnostics.Output)
	}

	thingLine, thingCharacter := sourcePosition(t, source, "func Use(value Thing)", "Thing")
	hover := executeLSPTestOperation(t, ctx, service, "hover", map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(hover.Output, "Thing") {
		t.Fatalf("hover = %s", hover.Output)
	}

	definition := executeLSPTestOperation(t, ctx, service, "definition", map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(definition.Output, "sample.go") {
		t.Fatalf("definition = %s", definition.Output)
	}

	references := executeLSPTestOperation(t, ctx, service, "references", map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter, "includeDeclaration": true,
	})
	if strings.Count(references.Output, "sample.go") < 2 {
		t.Fatalf("references = %s", references.Output)
	}

	symbols := executeLSPTestOperation(t, ctx, service, "symbols", map[string]any{"path": "sample.go"})
	if !strings.Contains(symbols.Output, `"name": "Thing"`) || !strings.Contains(symbols.Output, `"name": "Use"`) {
		t.Fatalf("symbols = %s", symbols.Output)
	}

	session := service.client.sessions["gopls"]
	if session == nil {
		t.Fatal("gopls session was not cached")
	}
	waitForLSPWatchRegistration(t, ctx, session.client.watcher)
	const externalSource = `package sample

func External(value Thing) int {
	return value.Value
}
`
	externalPath := filepath.Join(cwd, "external.go")
	if err := os.WriteFile(externalPath, []byte(externalSource), 0o644); err != nil {
		t.Fatal(err)
	}
	valueLine, valueCharacter := sourcePosition(t, source, "return value.Value", "Value")
	deadline := time.Now().Add(5 * time.Second)
	for {
		externalReferences := executeLSPTestOperation(t, ctx, service, "references", map[string]any{
			"path": "sample.go", "line": valueLine, "character": valueCharacter, "includeDeclaration": true,
		})
		if strings.Contains(externalReferences.Output, "external.go") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("external file change was not observed: %s", externalReferences.Output)
		}
		time.Sleep(lspWatchBatchDelay)
	}
	if service.client.sessions["gopls"] != session {
		t.Fatal("external file change restarted the gopls session")
	}

	rename := executeLSPTestOperation(t, ctx, service, "rename", map[string]any{
		"path": "sample.go", "line": valueLine + 1, "character": 81, "oldName": "Value", "newName": "Number",
	})
	if rename.Output != "renamed symbol in 3 files" {
		t.Fatalf("rename = %s", rename.Output)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), "Number") != 2 || strings.Contains(string(updated), "Value") {
		t.Fatalf("renamed source:\n%s", updated)
	}
	assertFileContent(t, testPath, strings.ReplaceAll(testSource, "Value", "Number"))
	assertFileContent(t, externalPath, strings.ReplaceAll(externalSource, "Value", "Number"))

	rename = executeLSPTestOperation(t, ctx, service, "rename", map[string]any{
		"path": "sample.go", "line": valueLine + 1, "character": 81, "oldName": "Number", "newName": "Value",
	})
	if rename.Output != "renamed symbol in 3 files" {
		t.Fatalf("reverse rename = %s", rename.Output)
	}
	assertFileContent(t, path, source)
	assertFileContent(t, testPath, testSource)
	assertFileContent(t, externalPath, externalSource)
}

func TestLSPImmediateConsecutiveRenamesWithGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}

	cwd := t.TempDir()
	writeLSPTestConfig(t, cwd)
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module example.com/rename\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const source = `package rename

var oldName = 1

func use() int {
	return oldName
}
`
	const testSource = `package rename

var testValue = oldName
`
	path := filepath.Join(cwd, "rename.go")
	testPath := filepath.Join(cwd, "rename_test.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}

	service, err := New(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	line, character := sourcePosition(t, source, "var oldName", "oldName")

	for _, names := range [][2]string{{"oldName", "NewName"}, {"NewName", "oldName"}} {
		rename := executeLSPTestOperation(t, ctx, service, "rename", map[string]any{
			"path": "rename.go", "line": line, "character": character, "oldName": names[0], "newName": names[1],
		})
		if rename.Output != "renamed symbol in 2 files" {
			t.Fatalf("rename %s to %s = %s", names[0], names[1], rename.Output)
		}
	}
	assertFileContent(t, path, source)
	assertFileContent(t, testPath, testSource)
}

func waitForLSPWatchRegistration(t *testing.T, ctx context.Context, watcher *lspWatchManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count, err := watcher.registrationCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for watched-files registration")
		}
		time.Sleep(lspWatchBatchDelay)
	}
}

type documentLifecycleServer struct {
	protocol.UnimplementedServer
	openErr         error
	closeErr        error
	closeContextErr error
	opened          int
	closed          int
}

func (s *documentLifecycleServer) DidOpen(ctx context.Context, _ *protocol.DidOpenTextDocumentParams) error {
	s.opened++
	if s.openErr != nil {
		return s.openErr
	}
	return ctx.Err()
}

func (s *documentLifecycleServer) DidClose(ctx context.Context, _ *protocol.DidCloseTextDocumentParams) error {
	s.closed++
	s.closeContextErr = ctx.Err()
	return s.closeErr
}

func TestLSPDocumentOpenFailureInvalidatesSession(t *testing.T) {
	openErr := errors.New("open failed")
	for _, test := range []struct {
		name    string
		openErr error
		cancel  bool
	}{
		{name: "server failure", openErr: openErr},
		{name: "canceled context", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &documentLifecycleServer{openErr: test.openErr}
			stopped := 0
			session := &lspSession{
				server:      server,
				client:      &lspProtocolClient{diagnostics: make(map[uri.URI][]protocol.Diagnostic), waiters: make(map[uri.URI][]chan []protocol.Diagnostic)},
				stopSession: func() { stopped++ },
			}
			config := lspServerConfig{name: "test", languageID: "go"}
			client := newLSPClient(t.TempDir(), nil)
			client.sessions[config.name] = session
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancel {
				cancel()
			} else {
				defer cancel()
			}

			requestCalled := false
			_, err := client.withOpenDocument(ctx, config, session, filepath.Join(t.TempDir(), "sample.go"), []byte("package sample"), func(context.Context, *lspSession, protocol.TextDocumentIdentifier) (any, error) {
				requestCalled = true
				return nil, nil
			})
			wantErr := test.openErr
			if test.cancel {
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) || requestCalled || server.opened != 1 || server.closed != 0 || stopped != 1 {
				t.Fatalf("error=%v requestCalled=%v opened=%d closed=%d stopped=%d", err, requestCalled, server.opened, server.closed, stopped)
			}
			if _, cached := client.sessions[config.name]; cached {
				t.Fatal("failed session remained cached")
			}
		})
	}
}

func TestLSPDocumentCleanupUsesLiveContextAndInvalidatesFailedSession(t *testing.T) {
	requestErr := errors.New("request failed")
	closeErr := errors.New("close failed")
	for _, test := range []struct {
		name        string
		requestErr  error
		closeErr    error
		wantStopped int
		wantCached  bool
	}{
		{name: "close succeeds", requestErr: requestErr, wantCached: true},
		{name: "request and close fail", requestErr: requestErr, closeErr: closeErr, wantStopped: 1},
		{name: "only close fails", closeErr: closeErr, wantStopped: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &documentLifecycleServer{closeErr: test.closeErr}
			stopped := 0
			session := &lspSession{
				server:      server,
				client:      &lspProtocolClient{diagnostics: make(map[uri.URI][]protocol.Diagnostic), waiters: make(map[uri.URI][]chan []protocol.Diagnostic)},
				stopSession: func() { stopped++ },
			}
			config := lspServerConfig{name: "test", languageID: "go"}
			client := newLSPClient(t.TempDir(), nil)
			client.sessions[config.name] = session
			ctx, cancel := context.WithCancel(context.Background())

			response, err := client.withOpenDocument(ctx, config, session, filepath.Join(t.TempDir(), "sample.go"), []byte("package sample"), func(context.Context, *lspSession, protocol.TextDocumentIdentifier) (any, error) {
				cancel()
				return "response", test.requestErr
			})
			if response != "response" || test.requestErr != nil && !errors.Is(err, test.requestErr) {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if test.closeErr != nil && !errors.Is(err, test.closeErr) {
				t.Fatalf("error = %v, want close failure", err)
			}
			if server.opened != 1 || server.closed != 1 || server.closeContextErr != nil {
				t.Fatalf("opened=%d closed=%d close context error=%v", server.opened, server.closed, server.closeContextErr)
			}
			if stopped != test.wantStopped {
				t.Fatalf("stopped=%d, want %d", stopped, test.wantStopped)
			}
			_, cached := client.sessions[config.name]
			if cached != test.wantCached {
				t.Fatalf("cached=%v, want %v", cached, test.wantCached)
			}
		})
	}
}

func TestLSPDiagnosticsCancellationRemovesWaiter(t *testing.T) {
	client := &lspProtocolClient{
		diagnostics: make(map[uri.URI][]protocol.Diagnostic),
		waiters:     make(map[uri.URI][]chan []protocol.Diagnostic),
	}
	documentURI := uri.File(filepath.Join(t.TempDir(), "sample.go"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.waitForDiagnostics(ctx, documentURI); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	if _, exists := client.waiters[documentURI]; exists {
		t.Fatal("canceled diagnostics waiter was retained")
	}
}

type blockingShutdownServer struct {
	protocol.UnimplementedServer
	done chan struct{}
}

func (s blockingShutdownServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	close(s.done)
	return ctx.Err()
}

func TestLSPShutdownIsBounded(t *testing.T) {
	server := blockingShutdownServer{done: make(chan struct{})}
	shutdownLSPServer(server, time.Millisecond)

	select {
	case <-server.done:
	default:
		t.Fatal("shutdown context did not expire")
	}
}

func TestLSPPositionOffsetUsesUTF16(t *testing.T) {
	content := []byte("a😀b\r\nnext")
	for _, test := range []struct {
		name     string
		position protocol.Position
		want     int
		wantErr  bool
	}{
		{name: "start", position: protocol.Position{}, want: 0},
		{name: "before surrogate pair", position: protocol.Position{Character: 1}, want: 1},
		{name: "after surrogate pair", position: protocol.Position{Character: 3}, want: 5},
		{name: "line end", position: protocol.Position{Character: 4}, want: 6},
		{name: "next line", position: protocol.Position{Line: 1}, want: 8},
		{name: "inside surrogate pair", position: protocol.Position{Character: 2}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := lspPositionOffset(content, test.position)
			if test.wantErr {
				if err == nil {
					t.Fatalf("offset = %d, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("offset=%d error=%v, want %d", got, err, test.want)
			}
		})
	}
}

func executeLSPTestOperation(t *testing.T, ctx context.Context, service *Service, name string, arguments map[string]any) operationResult {
	t.Helper()

	path, _ := arguments["path"].(string)
	line, _ := arguments["line"].(int)
	character, _ := arguments["character"].(int)
	var response any
	var err error
	switch name {
	case "diagnostics":
		response, err = service.Diagnostics(ctx, path)
	case "hover":
		response, err = service.Hover(ctx, path, line, character)
	case "definition":
		response, err = service.Definition(ctx, path, line, character)
	case "references":
		includeDeclaration, _ := arguments["includeDeclaration"].(bool)
		response, err = service.References(ctx, path, line, character, includeDeclaration)
	case "symbols":
		response, err = service.Symbols(ctx, path)
	case "rename":
		oldName, _ := arguments["oldName"].(string)
		newName, _ := arguments["newName"].(string)
		var changed int
		changed, err = service.Rename(ctx, path, line, character, oldName, newName)
		response = fmt.Sprintf("renamed symbol in %d files", changed)
	default:
		t.Fatalf("unknown operation %q", name)
	}
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := response.(string); ok {
		return operationResult{Output: text}
	}
	encoded, err := protocol.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := json.Indent(&output, encoded, "", "  "); err != nil {
		t.Fatal(err)
	}
	return operationResult{Output: output.String()}
}

type operationResult struct {
	Output string
}

func sourcePosition(t *testing.T, source, lineText, symbol string) (int, int) {
	t.Helper()

	lines := strings.Split(source, "\n")
	for line, text := range lines {
		if strings.Contains(text, lineText) {
			return line, strings.Index(text, symbol)
		}
	}
	t.Fatalf("line %q not found", lineText)
	return 0, 0
}
