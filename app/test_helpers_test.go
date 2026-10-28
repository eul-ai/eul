package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type providerFunction func(context.Context, agent.Request, agent.TextSink) (agent.Response, error)

func (function providerFunction) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return function(ctx, request, observer.Text)
}

func (providerFunction) RetryGeneration(error, int) (time.Duration, bool) { return 0, false }

func (providerFunction) ShouldCompact(agent.Request, agent.Usage) bool { return false }

func (providerFunction) Compact(context.Context, agent.Request) (agent.CompactResponse, error) {
	return agent.CompactResponse{}, errors.New("compaction is unavailable")
}

func (providerFunction) ShouldCompactAfterError(agent.Request, error) bool { return false }

func emptyProvider() providerFunction {
	return func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}
}

func testCatalog(models ...string) map[string]backend.ModelMetadata {
	catalog := make(map[string]backend.ModelMetadata, len(models))
	for _, model := range models {
		catalog[model] = backend.ModelMetadata{ThinkingLevels: agent.ThinkingLevels(), FastMode: true}
	}
	return catalog
}

func testBackendRuntime(provider agent.Provider) *backend.Runtime {
	return &backend.Runtime{
		Provider: provider,
		Catalog:  testCatalog("model", "fast-model", "balanced-model", "main-model", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"),
	}
}

func testBackend(id string, runtime *backend.Runtime) backend.Backend {
	return backend.Backend{
		ID:     id,
		Name:   "Test Provider",
		Models: backend.Models{Main: "gpt-5.6-sol", Fast: "gpt-5.6-luna", Balanced: "gpt-5.6-terra"},
		Open: func(context.Context, string) (*backend.Runtime, error) {
			return runtime, nil
		},
	}
}

// testLauncher runs in cwd with an empty user home.
func testLauncher(t *testing.T, cwd string, runtime *backend.Runtime) *launcher {
	t.Helper()
	t.Chdir(cwd)
	t.Setenv("HOME", t.TempDir())
	return newLauncher(t.TempDir(), backend.Registry{testBackend("test", runtime)}, false, nil)
}

// testSession stands in for the terminal: it runs prompts on an opened
// session and follows its updates.
type testSession struct {
	*session.Session
	subagents subagent.Status
}

func startTestSession(t *testing.T, launcher *launcher, settings session.Settings) *testSession {
	t.Helper()
	opened, err := launcher.start(context.Background(), settings, "")
	if err != nil {
		t.Fatal(err)
	}
	return &testSession{Session: opened}
}

func (s *testSession) next() (session.Update, error) {
	select {
	case update := <-s.Updates():
		if update.Kind == session.UpdateSubagents {
			s.subagents = update.Subagents
		}
		return update, nil
	case <-time.After(5 * time.Second):
		return session.Update{}, errors.New("session sent no update")
	}
}

// run sends prompt and waits for its turn to settle.
func (s *testSession) run(prompt string) error {
	if err := s.Send([]agent.ContentPart{{Kind: agent.ContentPartText, Text: prompt}}); err != nil {
		return err
	}
	for {
		update, err := s.next()
		switch {
		case err != nil:
			return err
		case update.Kind == session.UpdateSettled:
			return update.Err
		}
	}
}

func (s *testSession) waitSubagents(t *testing.T, done func(subagent.Status) bool) subagent.Status {
	t.Helper()
	for !done(s.subagents) {
		if _, err := s.next(); err != nil {
			t.Fatalf("%v; subagents = %+v", err, s.subagents)
		}
	}
	return s.subagents
}

func waitForSubagentCompletions(t *testing.T, s *testSession, count int) {
	t.Helper()
	s.waitSubagents(t, func(status subagent.Status) bool { return len(status.PendingCompletions) == count })
}

func savePrompt(t *testing.T, conversation terminal.Session, prompt string) {
	t.Helper()
	conversation.Append(session.EntryUser, prompt)
	if err := conversation.SaveDraft(session.Draft{}); err != nil {
		t.Fatal(err)
	}
}

// markActive rewrites saved session metadata as if a turn was running.
func markActive(t *testing.T, encoded []byte) []byte {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["active"] = true
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func sessionClosed(conversation terminal.Session) bool {
	return conversation.SaveDraft(session.Draft{}) != nil
}

func transcriptHas(conversation terminal.Session, kind session.EntryKind, text string) bool {
	entries, _ := conversation.Sync(nil)
	return slices.ContainsFunc(entries, func(entry session.Entry) bool {
		return entry.Kind == kind && strings.Contains(entry.Text, text)
	})
}

func sessionDirectory(t *testing.T, launcher *launcher, id string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(launcher.home, "sessions", "*", id))
	if err != nil || len(paths) != 1 {
		t.Fatalf("session %s directories = %v, error = %v", id, paths, err)
	}
	return paths[0]
}

// toolCaller is a provider for driving session tools. A main agent prompt of the
// form "call <tool> <arguments>" calls that tool, and the turn ends after its
// result. Subagent requests go to child.
type toolCaller struct {
	child   providerFunction
	request agent.Request
	result  agent.Input
}

func (caller *toolCaller) generate(ctx context.Context, request agent.Request, sink agent.TextSink) (agent.Response, error) {
	if strings.Contains(request.Instructions, childAgentInstructions) {
		return caller.child(ctx, request, sink)
	}

	caller.request = request
	for _, input := range request.Inputs {
		if input.Kind == agent.InputToolResult {
			caller.result = input
			return agent.Response{Text: "done"}, nil
		}
		call, ok := strings.CutPrefix(input.PlainText(), "call ")
		if input.Kind == agent.InputUser && ok {
			name, arguments, _ := strings.Cut(call, " ")
			return agent.Response{ToolCalls: []agent.ToolCall{{ID: "call", Name: name, Arguments: json.RawMessage(arguments)}}}, nil
		}
	}
	return agent.Response{Text: "done"}, nil
}

func (caller *toolCaller) runtime() *backend.Runtime {
	return testBackendRuntime(providerFunction(caller.generate))
}

func (caller *toolCaller) call(s *testSession, name, arguments string) (agent.Input, error) {
	err := s.run("call " + name + " " + arguments)
	return caller.result, err
}
