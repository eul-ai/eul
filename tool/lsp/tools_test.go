package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool"
)

func TestNewLSPOmitsToolsWhenServerIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if tools := New(t.TempDir()).Tools(); len(tools) != 0 {
		t.Fatalf("New() returned %d tools", len(tools))
	}
}

func TestNewLSPRegistersFullAndReadOnlyToolSets(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "gopls"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	for _, test := range []struct {
		name      string
		set       *Set
		wantNames []string
	}{
		{
			name:      "full",
			set:       New(t.TempDir()),
			wantNames: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName, lspRenameToolName},
		},
		{
			name:      "read-only",
			set:       NewReadOnly(t.TempDir()),
			wantNames: []string{lspDiagnosticsToolName, lspHoverToolName, lspDefinitionToolName, lspReferencesToolName, lspSymbolsToolName},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := test.set.Tools()
			if len(tools) != len(test.wantNames) {
				t.Fatalf("tool count = %d, want %d", len(tools), len(test.wantNames))
			}
			for index, current := range tools {
				if current.Definition().Name != test.wantNames[index] {
					t.Fatalf("tool %d = %q, want %q", index, current.Definition().Name, test.wantNames[index])
				}
			}
			stops := 0
			test.set.client.sessions["test"] = &lspSession{stopSession: func() { stops++ }}
			if err := test.set.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if err := test.set.Close(); err != nil {
				t.Fatalf("second Close() error = %v", err)
			}
			if stops != 1 {
				t.Fatalf("session stops = %d, want 1", stops)
			}
		})
	}
}

func TestLSPToolsWithGopls(t *testing.T) {
	if _, err := exec.LookPath(lspServerConfigs[0].command); err != nil {
		t.Skip("gopls is not installed")
	}

	cwd := t.TempDir()
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

	set := New(cwd)
	tools := set.Tools()
	if len(tools) != 6 {
		t.Fatalf("New() returned %d tools", len(tools))
	}
	registry, err := tool.NewRegistry(tools, set)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	diagnostics := executeLSPTestTool(t, ctx, registry, lspDiagnosticsToolName, map[string]any{"path": "sample.go"})
	if !strings.Contains(diagnostics.Output, `"items": []`) {
		t.Fatalf("diagnostics = %s", diagnostics.Output)
	}

	thingLine, thingCharacter := sourcePosition(t, source, "func Use(value Thing)", "Thing")
	hover := executeLSPTestTool(t, ctx, registry, lspHoverToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(hover.Output, "Thing") {
		t.Fatalf("hover = %s", hover.Output)
	}

	definition := executeLSPTestTool(t, ctx, registry, lspDefinitionToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter,
	})
	if !strings.Contains(definition.Output, "sample.go") {
		t.Fatalf("definition = %s", definition.Output)
	}

	references := executeLSPTestTool(t, ctx, registry, lspReferencesToolName, map[string]any{
		"path": "sample.go", "line": thingLine, "character": thingCharacter, "includeDeclaration": true,
	})
	if strings.Count(references.Output, "sample.go") < 2 {
		t.Fatalf("references = %s", references.Output)
	}

	symbols := executeLSPTestTool(t, ctx, registry, lspSymbolsToolName, map[string]any{"path": "sample.go"})
	if !strings.Contains(symbols.Output, `"name": "Thing"`) || !strings.Contains(symbols.Output, `"name": "Use"`) {
		t.Fatalf("symbols = %s", symbols.Output)
	}

	session := set.client.sessions[lspServerConfigs[0].name]
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
		externalReferences := executeLSPTestTool(t, ctx, registry, lspReferencesToolName, map[string]any{
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
	if set.client.sessions[lspServerConfigs[0].name] != session {
		t.Fatal("external file change restarted the gopls session")
	}

	rename := executeLSPTestTool(t, ctx, registry, lspRenameToolName, map[string]any{
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

	rename = executeLSPTestTool(t, ctx, registry, lspRenameToolName, map[string]any{
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
	if _, err := exec.LookPath(lspServerConfigs[0].command); err != nil {
		t.Skip("gopls is not installed")
	}

	cwd := t.TempDir()
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

	set := New(cwd)
	registry, err := tool.NewRegistry(set.Tools(), set)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	line, character := sourcePosition(t, source, "var oldName", "oldName")

	for _, names := range [][2]string{{"oldName", "NewName"}, {"NewName", "oldName"}} {
		rename := executeLSPTestTool(t, ctx, registry, lspRenameToolName, map[string]any{
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
			client := newLSPClient(t.TempDir())
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
			client := newLSPClient(t.TempDir())
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

func TestLSPToolDescriptionsAreServerAgnostic(t *testing.T) {
	for _, definition := range []agent.ToolDefinition{
		lspDiagnosticsToolDefinition,
		lspHoverToolDefinition,
		lspDefinitionToolDefinition,
		lspReferencesToolDefinition,
		lspSymbolsToolDefinition,
		lspRenameToolDefinition,
	} {
		for _, config := range lspServerConfigs {
			if strings.Contains(strings.ToLower(definition.Description), strings.ToLower(config.name)) {
				t.Fatalf("%s description names server %q: %s", definition.Name, config.name, definition.Description)
			}
		}
	}
}

func TestLSPDiagnosticsPresentationShowsPath(t *testing.T) {
	diagnostics := &lspTool{definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics}
	registry, err := tool.NewRegistry([]tool.Tool{diagnostics})
	if err != nil {
		t.Fatal(err)
	}

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		Name:         lspDiagnosticsToolName,
		RawArguments: `{"path":"sample.go"}`,
		Complete:     true,
	})
	if presentation.Title != lspDiagnosticsToolName || presentation.Arguments != "sample.go" {
		t.Fatalf("diagnostics presentation = %+v", presentation)
	}
}

func TestLSPRenamePresentationShowsNames(t *testing.T) {
	rename := &lspTool{definition: lspRenameToolDefinition, operation: lspRename}
	registry, err := tool.NewRegistry([]tool.Tool{rename})
	if err != nil {
		t.Fatal(err)
	}

	presentation := registry.Presentation(agent.ToolCallSnapshot{
		Name:         lspRenameToolName,
		RawArguments: `{"path":"sample.go","line":1,"character":2,"oldName":"Value","newName":"Number"}`,
		Complete:     true,
	})
	if presentation.Title != lspRenameToolName || presentation.Arguments != "Value → Number" {
		t.Fatalf("rename presentation = %+v", presentation)
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

func executeLSPTestTool(t *testing.T, ctx context.Context, registry *tool.Registry, name string, arguments any) agent.ToolResult {
	t.Helper()

	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(ctx, agent.ToolCall{ID: "call", Name: name, Arguments: encoded}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s: %s", name, result.Output)
	}
	return result
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
