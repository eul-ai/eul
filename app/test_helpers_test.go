package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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

func startTestSession(t *testing.T, launcher *launcher, settings sessionSettings) *agentSession {
	t.Helper()
	session, err := launcher.start(context.Background(), settings, "")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func sessionStoreTestAgentCheckpoint(t testing.TB) agent.Checkpoint {
	t.Helper()
	var checkpoint agent.Checkpoint
	if err := json.Unmarshal([]byte(`{"version":3,"context_usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`), &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func sessionStoreTestTerminalCheckpoint(t testing.TB, prompt string) terminal.Checkpoint {
	t.Helper()
	return sessionStoreTestTerminalBlocks(t, []map[string]any{{
		"kind": 0,
		"text": prompt,
	}})
}

func sessionStoreTestTerminalBlocks(t testing.TB, blocks []map[string]any) terminal.Checkpoint {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"version": 2,
		"blocks":  blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint terminal.Checkpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func waitForSubagentCompletions(t *testing.T, manager *subagent.Manager, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(manager.Snapshot().PendingCompletions) == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending completions = %d, want %d", len(manager.Snapshot().PendingCompletions), count)
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

func (caller *toolCaller) call(session *agentSession, name, arguments string) (agent.Input, error) {
	_, err := session.engine.Run(context.Background(), []agent.ContentPart{{Kind: agent.ContentPartText, Text: "call " + name + " " + arguments}}, func(agent.Event) error { return nil })
	return caller.result, err
}
