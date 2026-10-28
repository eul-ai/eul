package app

import (
	"context"
	"encoding/json"
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
	terminalCheckpoint := testTerminalCheckpoint("first prompt")
	if err := first.terminalOptions.StateChanges.Notify(terminalCheckpoint, false); err != nil {
		t.Fatal(err)
	}
	sessionID := first.saver.handle.record.ID
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}

	second, err := launcher.resume(context.Background(), cwd, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.terminalOptions.Config.InitialCheckpoint == nil || session.Description(second.terminalOptions.Config.InitialCheckpoint.Entries) != "first prompt" {
		t.Fatalf("terminal checkpoint = %+v", second.terminalOptions.Config.InitialCheckpoint)
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
	session := startTestSession(t, testLauncher(t, cwd, caller.runtime()), sessionSettings{models: backend.Models{Main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd})
	defer session.finish(nil)
	if err := session.terminalOptions.StateChanges.Notify(testTerminalCheckpoint("prompt"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.call(session, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect"}]}`); err != nil {
		t.Fatal(err)
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := readSessionRecord(session.saver.handle.path)
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
	caller := &toolCaller{child: func(ctx context.Context, _ agent.Request, _ agent.TextSink) (agent.Response, error) {
		<-ctx.Done()
		return agent.Response{}, ctx.Err()
	}}
	launcher := testLauncher(t, cwd, caller.runtime())

	first := startTestSession(t, launcher, sessionSettings{models: backend.Models{Main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd})
	if _, err := caller.call(first, "launch_subagents", `{"tasks":[{"description":"running","prompt":"running"}]}`); err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := testTerminalCheckpoint("active prompt")
	if err := first.terminalOptions.StateChanges.Notify(terminalCheckpoint, true); err != nil {
		t.Fatal(err)
	}
	sessionID := first.saver.handle.record.ID
	first.saver.stop()
	first.subagents.Close()
	if err := first.saver.handle.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := launcher.resume(context.Background(), cwd, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !second.terminalOptions.Config.PreviousTurnActive {
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
	alternateRuntime := testBackendRuntime(emptyProvider())
	alternateRuntime.Catalog = testCatalog("selected-model", "selected-fast-model", "selected-balanced-model")
	launcher := testLauncher(t, cwd, testBackendRuntime(emptyProvider()))
	launcher.backends = append(launcher.backends, testBackend("alternate", alternateRuntime))
	models := backend.Models{Main: "selected-model", Fast: "selected-fast-model", Balanced: "selected-balanced-model"}

	session, err := launcher.launch(context.Background(), Options{Provider: "alternate", Models: models, ThinkingLevel: agent.ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if session.config.provider != "alternate" {
		t.Fatalf("provider = %q", session.config.provider)
	}
	if err := session.terminalOptions.StateChanges.Notify(testTerminalCheckpoint("saved prompt"), false); err != nil {
		t.Fatal(err)
	}
	sessionID := session.saver.handle.record.ID
	if err := session.finish(nil); err != nil {
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
