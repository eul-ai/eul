package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

func TestStoredAgentSessionRestoresProviderAndTerminalState(t *testing.T) {
	cwd := t.TempDir()
	var requests []agent.Request
	runtime := environment{
		stdin:  strings.NewReader(""),
		stdout: io.Discard,
		newToolset: func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error) {
			return tool.NewRegistry(nil)
		},
	}
	newBackendRuntime := func() *fakeBackendRuntime {
		return &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
			return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
				requests = append(requests, request)
				if len(requests) == 1 {
					return agent.Response{Text: "first answer", State: []byte("saved-state")}, nil
				}
				return agent.Response{Text: "second answer", State: []byte("next-state")}, nil
			}), nil
		}}
	}
	config := resolvedConfig{provider: "test", models: modelSet{main: "model", fast: "model", balanced: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	home := t.TempDir()
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)

	first, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.engine.Run(context.Background(), "first prompt", func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := sessionStoreTestTerminalCheckpoint(t, "first prompt")
	if err := first.terminalOptions.StateChanges.Notify(terminalCheckpoint, false); err != nil {
		t.Fatal(err)
	}
	sessionID := first.persistence.handle.record.ID
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}

	handle, err := store.Open(context.Background(), cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, messageHistory, handle)
	if err != nil {
		t.Fatal(err)
	}
	if second.terminalOptions.Config.InitialCheckpoint == nil || second.terminalOptions.Config.InitialCheckpoint.Description() != "first prompt" {
		t.Fatalf("terminal checkpoint = %+v", second.terminalOptions.Config.InitialCheckpoint)
	}
	summaries, warnings, err := second.terminalOptions.Sessions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(summaries) != 0 {
		t.Fatalf("current session was listed: %+v", summaries)
	}
	if _, err := second.engine.Run(context.Background(), "next prompt", func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := second.finish(nil); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 2 || requests[0].SessionID != sessionID || requests[1].SessionID != sessionID || string(requests[1].State) != "saved-state" || len(requests[1].Inputs) != 1 || requests[1].Inputs[0].PlainText() != "next prompt" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestStoredAgentSessionPersistsCompletionWhileParentIsIdle(t *testing.T) {
	cwd := t.TempDir()
	runtime := environment{
		stdin:  strings.NewReader(""),
		stdout: io.Discard,
		newToolset: func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error) {
			return tool.NewRegistry(nil)
		},
	}
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			return agent.Response{Text: "result for " + request.Inputs[0].PlainText()}, nil
		}), nil
	}}
	config := resolvedConfig{provider: "test", models: modelSet{main: "model", fast: "model", balanced: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	home := t.TempDir()
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)
	session, err := newStoredAgentSession(config, runtime, backendRuntime, store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
	if err := session.terminalOptions.StateChanges.Notify(sessionStoreTestTerminalCheckpoint(t, "prompt"), false); err != nil {
		t.Fatal(err)
	}
	launch := tool.NewLaunchSubagents(session.subagents)
	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`), nil); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := readSessionRecord(session.persistence.handle.path)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(record.Subagent)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"status":"complete"`) && strings.Contains(string(encoded), "result for inspect") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle completion was not persisted: %s", encoded)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStoredAgentSessionPersistsInterruptedSubagentsAsIdleOnRestore(t *testing.T) {
	cwd := t.TempDir()
	runtime := environment{
		stdin:  strings.NewReader(""),
		stdout: io.Discard,
		newToolset: func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error) {
			return tool.NewRegistry(nil)
		},
	}
	newBackendRuntime := func() *fakeBackendRuntime {
		return &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
			return providerFunction(func(ctx context.Context, _ agent.Request, _ agent.TextSink) (agent.Response, error) {
				<-ctx.Done()
				return agent.Response{}, ctx.Err()
			}), nil
		}}
	}
	config := resolvedConfig{provider: "test", models: modelSet{main: "model", fast: "model", balanced: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	home := t.TempDir()
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)

	first, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
	launch := tool.NewLaunchSubagents(first.subagents)
	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"running","prompt":"running"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := sessionStoreTestTerminalCheckpoint(t, "active prompt")
	if err := first.terminalOptions.StateChanges.Notify(terminalCheckpoint, true); err != nil {
		t.Fatal(err)
	}
	sessionID := first.persistence.handle.record.ID
	first.persistence.checkpoints.Stop()
	if err := first.subagents.Close(); err != nil {
		t.Fatal(err)
	}
	<-first.persistence.changesDone
	if err := first.persistence.checkpoints.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closeBackendRuntime(first.backendRuntime); err != nil {
		t.Fatal(err)
	}

	handle, err := store.Open(context.Background(), cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, messageHistory, handle)
	if err != nil {
		t.Fatal(err)
	}
	if !second.terminalOptions.Config.PreviousTurnActive {
		t.Fatal("previous active turn was not reported")
	}
	if err := second.finish(nil); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.record.Status != sessionIdle {
		t.Fatalf("restored status = %q", reopened.record.Status)
	}
	encoded, err := json.Marshal(reopened.record.Subagent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"status":"interrupted"`) || strings.Contains(string(encoded), `"active"`) {
		t.Fatalf("restored subagents = %s", encoded)
	}
}

func TestStoredSessionSelectsPersistedBackend(t *testing.T) {
	cwd := t.TempDir()
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr)
	defaultDriver := testBackendDriver(t, runtime)
	alternateRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	alternateDriver := &fakeBackendDriver{
		descriptor: backend.Descriptor{ID: "alternate", Name: "Alternate", DefaultModels: backend.ModelDefaults{Main: "alternate-model"}},
		runtime:    alternateRuntime,
	}
	registry, err := backend.NewRegistry("test", defaultDriver, alternateDriver)
	if err != nil {
		t.Fatal(err)
	}
	runtime.backends = registry
	home := t.TempDir()
	store := newSessionStore(home)
	messageHistory := newMessageHistoryStore(home)

	config, handle, selected, err := resolveInitialSession(context.Background(), Options{
		Provider:      "alternate",
		Model:         stringPointer("selected-model"),
		FastModel:     stringPointer("selected-fast-model"),
		BalancedModel: stringPointer("selected-balanced-model"),
		ThinkingLevel: agent.ThinkingHigh,
	}, runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	if handle != nil || config.provider != "alternate" || selected.Descriptor().ID != "alternate" {
		t.Fatalf("config=%+v handle=%v provider=%q", config, handle, selected.Descriptor().ID)
	}
	backendRuntime, err := selected.Open(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := newStoredAgentSession(config, runtime, backendRuntime, store, messageHistory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.terminalOptions.StateChanges.Notify(sessionStoreTestTerminalCheckpoint(t, "saved prompt"), false); err != nil {
		t.Fatal(err)
	}
	sessionID := session.persistence.handle.record.ID
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}

	restored, handle, selected, err := resolveInitialSession(context.Background(), Options{
		Resume:    true,
		SessionID: sessionID,
	}, runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if restored.provider != "alternate" || restored.models != (modelSet{main: "selected-model", fast: "selected-fast-model", balanced: "selected-balanced-model"}) || selected.Descriptor().ID != "alternate" {
		t.Fatalf("restored=%+v provider=%q", restored, selected.Descriptor().ID)
	}
}

func TestResolveStoredSessionSurfacesSkippedSessionWarnings(t *testing.T) {
	cwd := t.TempDir()
	store := newSessionStore(t.TempDir())
	corrupt, err := store.Create("test", cwd, modelSet{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "corrupt"), false)
	if err != nil {
		t.Fatal(err)
	}
	corruptPath := corrupt.path
	if err := corrupt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	valid, err := store.Create("test", cwd, modelSet{main: "model", fast: "persisted-fast", balanced: "persisted-balanced"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "valid"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr)
	runtime.userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }
	config, handle, _, err := resolveStoredSession(context.Background(), store, runtime, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if len(config.warnings) != 1 {
		t.Fatalf("warnings = %v", config.warnings)
	}
	if config.models != (modelSet{main: "model", fast: "persisted-fast", balanced: "persisted-balanced"}) {
		t.Fatalf("restored models = %+v", config)
	}
}
