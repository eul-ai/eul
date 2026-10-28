package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
)

func TestStoredAgentSessionRestoresProviderAndTerminalState(t *testing.T) {
	cwd := t.TempDir()
	var requests []agent.Request
	launcher := testLauncher(t, cwd, testBackendRuntime(providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return agent.Response{Text: "first answer", State: []byte("saved-state"), Usage: agent.Usage{TotalTokens: 42}}, nil
		}
		return agent.Response{Text: "second answer", State: []byte("next-state")}, nil
	})))

	first := startTestSession(t, launcher, sessionSettings{models: backend.Models{Main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd})
	if _, err := first.engine.Run(context.Background(), []agent.ContentPart{{Kind: agent.ContentPartText, Text: "first prompt"}}, func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	savePrompt(t, first.terminalOptions, "first prompt", false)
	sessionID := first.terminalOptions.Config.SessionID
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}

	second, err := launcher.resume(context.Background(), cwd, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptHas(second.terminalOptions, session.EntryUser, "first prompt") {
		t.Fatal("restored transcript omits the first prompt")
	}
	if second.terminalOptions.Config.ContextTokens != 42 {
		t.Fatalf("context tokens = %d", second.terminalOptions.Config.ContextTokens)
	}
	summaries, err := second.terminalOptions.Sessions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("current session was listed: %+v", summaries)
	}
	if _, err := second.engine.Run(context.Background(), []agent.ContentPart{{Kind: agent.ContentPartText, Text: "next prompt"}}, func(agent.Event) error { return nil }); err != nil {
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
	release := make(chan struct{})
	caller := &toolCaller{child: func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
		<-release
		return agent.Response{Text: "result for " + request.Inputs[0].PlainText()}, nil
	}}
	launcher := testLauncher(t, cwd, caller.runtime())
	idle := startTestSession(t, launcher, sessionSettings{models: backend.Models{Main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd})
	defer idle.finish(nil)
	savePrompt(t, idle.terminalOptions, "prompt", false)
	if _, err := caller.call(idle, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect"}]}`); err != nil {
		t.Fatal(err)
	}
	close(release)

	statePath := filepath.Join(sessionDirectory(t, launcher, idle.terminalOptions.Config.SessionID), "state.json")
	deadline := time.Now().Add(2 * time.Second)
	for {
		encoded, err := os.ReadFile(statePath)
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
	caller := &toolCaller{child: func(ctx context.Context, _ agent.Request, _ agent.TextSink) (agent.Response, error) {
		<-ctx.Done()
		return agent.Response{}, ctx.Err()
	}}
	launcher := testLauncher(t, cwd, caller.runtime())

	first := startTestSession(t, launcher, sessionSettings{models: backend.Models{Main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd})
	if _, err := caller.call(first, "launch_subagents", `{"tasks":[{"description":"running","prompt":"running"}]}`); err != nil {
		t.Fatal(err)
	}
	savePrompt(t, first.terminalOptions, "active prompt", true)
	sessionID := first.terminalOptions.Config.SessionID
	directory := sessionDirectory(t, launcher, sessionID)
	crashed := make(map[string][]byte)
	for _, name := range []string{"meta.json", "state.json"} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		crashed[name] = content
	}
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}
	for name, content := range crashed {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	second, err := launcher.resume(context.Background(), cwd, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptHas(second.terminalOptions, session.EntryNotice, "") {
		t.Fatal("previous active turn was not reported")
	}
	if err := second.finish(nil); err != nil {
		t.Fatal(err)
	}

	reopened, err := launcher.store.Open(cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Record().Active {
		t.Fatal("restored session is still active")
	}
	encoded, err := json.Marshal(reopened.Record().Subagent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"status":"interrupted"`) || strings.Contains(string(encoded), `"active"`) {
		t.Fatalf("restored subagents = %s", encoded)
	}
}

func TestStoredSessionSelectsPersistedBackend(t *testing.T) {
	cwd := t.TempDir()
	alternateRuntime := testBackendRuntime(emptyProvider())
	alternateRuntime.Catalog = testCatalog("selected-model", "selected-fast-model", "selected-balanced-model")
	launcher := testLauncher(t, cwd, testBackendRuntime(emptyProvider()))
	launcher.backends = append(launcher.backends, testBackend("alternate", alternateRuntime))
	models := backend.Models{Main: "selected-model", Fast: "selected-fast-model", Balanced: "selected-balanced-model"}

	selected, err := launcher.launch(context.Background(), Options{Provider: "alternate", Models: models, ThinkingLevel: agent.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if selected.config.provider != "alternate" {
		t.Fatalf("provider = %q", selected.config.provider)
	}
	savePrompt(t, selected.terminalOptions, "saved prompt", false)
	sessionID := selected.terminalOptions.Config.SessionID
	if err := selected.finish(nil); err != nil {
		t.Fatal(err)
	}

	restored, err := launcher.launch(context.Background(), Options{Resume: true, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.finish(nil)
	if restored.config.provider != "alternate" || restored.config.models != models {
		t.Fatalf("restored = %+v", restored.config)
	}
}
