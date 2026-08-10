package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	openaiadapter "github.com/eul-ai/eul/provider/openai"
	"github.com/eul-ai/eul/tool"
)

type closeRecordingTool struct {
	closeErr error
	closed   int
}

type usageCapableProvider struct {
	providerFunction
}

func (usageCapableProvider) Usage(context.Context) (agent.ProviderUsage, error) {
	return agent.ProviderUsage{}, nil
}

func (usageCapableProvider) ModelMetadata(string) agent.ModelMetadata {
	return agent.ModelMetadata{
		ContextWindow:  123_000,
		ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
	}
}

func (*closeRecordingTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "close-recording"}
}

func (*closeRecordingTool) Execute(context.Context, json.RawMessage, agent.ToolUpdateSink) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

func (current *closeRecordingTool) Close() error {
	current.closed++
	return current.closeErr
}

func TestNewAgentSessionWiresOptionalProviderUsage(t *testing.T) {
	provider := usageCapableProvider{providerFunction: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}}
	runtime := appRuntime{newProvider: func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error) {
		return provider, nil
	}}
	cwd := t.TempDir()
	writeMainTestLSPConfig(t, cwd)
	skills := []agent.Skill{{Name: "review", Description: "Review code"}}
	session, err := newAgentSession(agentConfig{model: "model", thinkingLevel: agent.ThinkingMedium, cwd: cwd, skills: skills}, runtime, nil, openaiadapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.tools.Close()
	if session.terminalOptions.LoadUsage == nil {
		t.Fatal("provider usage was not wired to the terminal")
	}
	if session.terminalOptions.SubagentUpdates == nil {
		t.Fatal("subagent status was not wired to the terminal")
	}
	if session.terminalOptions.WorkingDirectory != cwd {
		t.Fatalf("terminal working directory = %q, want %q", session.terminalOptions.WorkingDirectory, cwd)
	}
	if session.terminalOptions.ContextWindow != 123_000 || session.terminalOptions.ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("terminal metadata = %+v", session.terminalOptions)
	}
	if len(session.terminalOptions.Skills) != 1 || session.terminalOptions.Skills[0].Name != "review" {
		t.Fatalf("terminal skills = %+v", session.terminalOptions.Skills)
	}
}

func TestNewAgentSessionWiresUpdateGoalToEngine(t *testing.T) {
	runtime := appRuntime{newProvider: func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	cwd := t.TempDir()
	writeMainTestLSPConfig(t, cwd)
	session, err := newAgentSession(agentConfig{model: "model", cwd: cwd}, runtime, nil, openaiadapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.tools.Close()
	if err := session.engine.SetGoal("finish"); err != nil {
		t.Fatal(err)
	}

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "complete", Name: "update_goal", Arguments: json.RawMessage(`{"status":"complete"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	goal, ok := session.engine.Goal()
	if result.IsError || !ok || !goal.Complete {
		t.Fatalf("result=%+v goal=%+v exists=%v", result, goal, ok)
	}
}

func TestStoredAgentSessionRestoresProviderAndTerminalState(t *testing.T) {
	cwd := t.TempDir()
	var requests []agent.Request
	runtime := appRuntime{
		stdin:  strings.NewReader(""),
		stdout: io.Discard,
		newProvider: func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error) {
			return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
				requests = append(requests, request)
				if len(requests) == 1 {
					return agent.Response{Text: "first answer", State: []byte("saved-state")}, nil
				}
				return agent.Response{Text: "second answer", State: []byte("next-state")}, nil
			}), nil
		},
		newToolset: func(string, toolAccess, ...tool.Tool) (*tool.Registry, error) {
			return tool.NewRegistry(nil)
		},
	}
	config := agentConfig{model: "model", thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	store := newSessionStore(t.TempDir())

	first, err := newStoredAgentSession(config, runtime, nil, openaiadapter.Options{}, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.engine.Run(context.Background(), "first prompt", func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	agentCheckpoint, err := first.engine.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := sessionStoreTestTerminalCheckpoint(t, "first prompt")
	if err := first.terminalOptions.SaveCheckpoint(agentCheckpoint, terminalCheckpoint, false); err != nil {
		t.Fatal(err)
	}
	sessionID := first.persistence.record.ID
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}

	handle, err := store.Open(context.Background(), cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoredAgentSession(config, runtime, nil, openaiadapter.Options{}, store, handle)
	if err != nil {
		t.Fatal(err)
	}
	if second.terminalOptions.InitialCheckpoint == nil || second.terminalOptions.InitialCheckpoint.Description() != "first prompt" {
		t.Fatalf("terminal checkpoint = %+v", second.terminalOptions.InitialCheckpoint)
	}
	summaries, err := second.terminalOptions.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
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

	if len(requests) != 2 || string(requests[1].State) != "saved-state" || len(requests[1].Inputs) != 1 || requests[1].Inputs[0].Text != "next prompt" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestNewAgentSessionReportsToolsetConfigurationFailure(t *testing.T) {
	configureErr := errors.New("toolset failed")
	runtime := appRuntime{
		newProvider: func(openaiadapter.CodexTokenSource, openaiadapter.Options) (agent.Provider, error) {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		},
		newToolset: func(string, toolAccess, ...tool.Tool) (*tool.Registry, error) {
			return nil, configureErr
		},
	}

	_, err := newAgentSession(agentConfig{model: "model", cwd: t.TempDir()}, runtime, nil, openaiadapter.Options{})
	if !errors.Is(err, configureErr) || !strings.Contains(err.Error(), "configure tools") {
		t.Fatalf("newAgentSession error = %v", err)
	}
}

func TestFinishRegistryClosesToolsAndPreservesRunError(t *testing.T) {
	runErr := context.Canceled
	closeErr := errors.New("close failed")
	closer := &closeRecordingTool{closeErr: closeErr}
	registry, err := tool.NewRegistry([]tool.Tool{closer})
	if err != nil {
		t.Fatal(err)
	}

	err = finishRegistry(runErr, registry, "close subagent tools")
	if closer.closed != 1 {
		t.Fatalf("close calls = %d, want 1", closer.closed)
	}
	if !errors.Is(err, runErr) || !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "close subagent tools") {
		t.Fatalf("joined error = %v", err)
	}
}

func TestFinishRunReportsCleanupFailureJoinedWithInterruption(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	var output bytes.Buffer

	code := finishRun(errors.Join(context.Canceled, cleanupErr), &output)
	if code != exitFailure || !strings.Contains(output.String(), cleanupErr.Error()) {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestAgentSessionFinishClosesOwnedRegistry(t *testing.T) {
	closer := &closeRecordingTool{}
	registry, err := tool.NewRegistry([]tool.Tool{closer})
	if err != nil {
		t.Fatal(err)
	}
	session := &agentSession{tools: registry}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
	if closer.closed != 1 {
		t.Fatalf("close calls = %d, want 1", closer.closed)
	}
}
