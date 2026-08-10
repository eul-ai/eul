package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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

type metadataFreeProvider struct{}

func (metadataFreeProvider) Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error) {
	return agent.Response{}, nil
}

type profileMetadataProvider struct {
	providerFunction
	metadata  map[string]agent.ModelMetadata
	requested []string
}

func (provider *profileMetadataProvider) ModelMetadata(model string) agent.ModelMetadata {
	provider.requested = append(provider.requested, model)
	return provider.metadata[model]
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

func TestAgentConfigSelectsSubagentModelProfiles(t *testing.T) {
	config := agentConfig{
		model:                 "powerful-model",
		subagentBalancedModel: "balanced-model",
		subagentFastModel:     "fast-model",
	}
	for _, test := range []struct {
		profile tool.SubagentModelProfile
		want    string
	}{
		{profile: tool.SubagentModelFast, want: "fast-model"},
		{profile: tool.SubagentModelBalanced, want: "balanced-model"},
		{profile: tool.SubagentModelPowerful, want: "powerful-model"},
	} {
		if got := config.subagentModel(test.profile); got != test.want {
			t.Fatalf("profile %q selected %q, want %q", test.profile, got, test.want)
		}
	}
}

func TestProviderModelMetadataDefaultsToThinkingOff(t *testing.T) {
	metadata := providerModelMetadata(metadataFreeProvider{}, "model")
	if !slices.Equal(metadata.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("thinking levels = %v", metadata.ThinkingLevels)
	}
}

func TestNewAgentSessionWiresOptionalProviderUsage(t *testing.T) {
	provider := usageCapableProvider{providerFunction: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}}
	runtime := appRuntime{}
	backendInstance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
		return provider, nil
	}}
	cwd := t.TempDir()
	writeMainTestLSPConfig(t, cwd)
	skills := []agent.Skill{{Name: "review", Description: "Review code"}}
	warnings := []string{"Skipped skill invalid: malformed"}
	session, err := newAgentSession(agentConfig{model: "model", thinkingLevel: agent.ThinkingMedium, cwd: cwd, skills: skills, warnings: warnings}, runtime, backendInstance)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
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
	if !slices.Equal(session.terminalOptions.Warnings, warnings) {
		t.Fatalf("terminal warnings = %v", session.terminalOptions.Warnings)
	}
}

func TestNewAgentSessionUsesMetadataForEachModelProfile(t *testing.T) {
	provider := &profileMetadataProvider{
		providerFunction: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{Text: "done"}, nil
		},
		metadata: map[string]agent.ModelMetadata{
			"main":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
			"fast":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}},
			"balanced": {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
		},
	}
	instance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
		return provider, nil
	}}
	runtime := appRuntime{newToolset: func(_ string, _ toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
		return tool.NewRegistry(additional)
	}}
	session, err := newAgentSession(agentConfig{
		model:                 "main",
		subagentFastModel:     "fast",
		subagentBalancedModel: "balanced",
		thinkingLevel:         agent.ThinkingHigh,
		cwd:                   t.TempDir(),
	}, runtime, instance)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(provider.requested, []string{"main", "fast", "balanced"}) {
		t.Fatalf("metadata requests = %v", provider.requested)
	}

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "fast", Name: "subagent", Arguments: json.RawMessage(`{"tasks":["inspect"],"model_profile":"fast","thinking_level":"high"}`),
	}, nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "fast model") {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	result, err = session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "balanced", Name: "subagent", Arguments: json.RawMessage(`{"tasks":["inspect"],"model_profile":"balanced","thinking_level":"high"}`),
	}, nil)
	if err != nil || result.IsError {
		t.Fatalf("balanced result = %+v, error = %v", result, err)
	}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
	if instance.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", instance.closeCalls)
	}
}

func TestNewAgentSessionWiresUpdateGoalToEngine(t *testing.T) {
	runtime := appRuntime{}
	backendInstance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	cwd := t.TempDir()
	writeMainTestLSPConfig(t, cwd)
	session, err := newAgentSession(agentConfig{model: "model", cwd: cwd}, runtime, backendInstance)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
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
		newToolset: func(string, toolAccess, ...tool.Tool) (*tool.Registry, error) {
			return tool.NewRegistry(nil)
		},
	}
	newBackendInstance := func() *fakeBackendInstance {
		return &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
			return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
				requests = append(requests, request)
				if len(requests) == 1 {
					return agent.Response{Text: "first answer", State: []byte("saved-state")}, nil
				}
				return agent.Response{Text: "second answer", State: []byte("next-state")}, nil
			}), nil
		}}
	}
	config := agentConfig{provider: "test", model: "model", thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	store := newSessionStore(t.TempDir())

	first, err := newStoredAgentSession(config, runtime, newBackendInstance(), store, nil)
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
	second, err := newStoredAgentSession(config, runtime, newBackendInstance(), store, handle)
	if err != nil {
		t.Fatal(err)
	}
	if second.terminalOptions.InitialCheckpoint == nil || second.terminalOptions.InitialCheckpoint.Description() != "first prompt" {
		t.Fatalf("terminal checkpoint = %+v", second.terminalOptions.InitialCheckpoint)
	}
	summaries, warnings, err := second.terminalOptions.ListSessions(context.Background())
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

	if len(requests) != 2 || string(requests[1].State) != "saved-state" || len(requests[1].Inputs) != 1 || requests[1].Inputs[0].Text != "next prompt" {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestStoredSessionSelectsPersistedBackend(t *testing.T) {
	cwd := t.TempDir()
	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	defaultDriver := testBackendDriver(t, runtime)
	alternateInstance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	alternateDriver := &fakeBackendDriver{
		descriptor: backend.Descriptor{ID: "alternate", Name: "Alternate"},
		defaults:   backend.ModelDefaults{Main: "alternate-model"},
		instance:   alternateInstance,
	}
	registry, err := backend.NewRegistry("test", defaultDriver, alternateDriver)
	if err != nil {
		t.Fatal(err)
	}
	runtime.backends = registry
	store := newSessionStore(t.TempDir())

	config, handle, selected, err := resolveInitialSession(context.Background(), agentArguments{
		provider:         "alternate",
		model:            "selected-model",
		modelSet:         true,
		fastModel:        "selected-fast-model",
		fastModelSet:     true,
		balancedModel:    "selected-balanced-model",
		balancedModelSet: true,
		thinkingLevel:    agent.ThinkingHigh,
	}, runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	if handle != nil || config.provider != "alternate" || selected.Descriptor().ID != "alternate" {
		t.Fatalf("config=%+v handle=%v provider=%q", config, handle, selected.Descriptor().ID)
	}
	configured, err := selected.Configure(backend.Options{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := newStoredAgentSession(config, runtime, configured, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentCheckpoint, err := session.engine.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.terminalOptions.SaveCheckpoint(agentCheckpoint, sessionStoreTestTerminalCheckpoint(t, "saved prompt"), false); err != nil {
		t.Fatal(err)
	}
	sessionID := session.persistence.record.ID
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}

	restored, handle, selected, err := resolveInitialSession(context.Background(), agentArguments{
		resume:    true,
		sessionID: sessionID,
	}, runtime, store)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if restored.provider != "alternate" || restored.model != "selected-model" || restored.subagentFastModel != "selected-fast-model" || restored.subagentBalancedModel != "selected-balanced-model" || selected.Descriptor().ID != "alternate" {
		t.Fatalf("restored=%+v provider=%q", restored, selected.Descriptor().ID)
	}
}

func TestResolveStoredSessionSurfacesSkippedSessionWarnings(t *testing.T) {
	cwd := t.TempDir()
	store := newSessionStore(t.TempDir())
	corrupt, err := store.Create("test", cwd, "model", "fast-model", "balanced-model", agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "corrupt"))
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
	valid, err := store.Create("test", cwd, "model", "", "", agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), sessionStoreTestTerminalCheckpoint(t, "valid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runtime := testRuntime(cwd, &stdout, &stderr, nil)
	runtime.userHomeDir = func() (string, error) { return "", errors.New("home unavailable") }
	config, handle, _, err := resolveStoredSession(context.Background(), store, runtime, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if len(config.warnings) != 1 || !strings.Contains(config.warnings[0], "Skipped session") || !strings.Contains(config.warnings[0], "unsupported session version") {
		t.Fatalf("warnings = %v", config.warnings)
	}
	if config.subagentFastModel != "gpt-5.6-luna" || config.subagentBalancedModel != "gpt-5.6-terra" {
		t.Fatalf("restored default models = %+v", config)
	}
}

func TestNewAgentSessionReportsToolsetConfigurationFailure(t *testing.T) {
	configureErr := errors.New("toolset failed")
	runtime := appRuntime{
		newToolset: func(string, toolAccess, ...tool.Tool) (*tool.Registry, error) {
			return nil, configureErr
		},
	}

	backendInstance := &fakeBackendInstance{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	_, err := newAgentSession(agentConfig{model: "model", cwd: t.TempDir()}, runtime, backendInstance)
	if !errors.Is(err, configureErr) || !strings.Contains(err.Error(), "configure tools") {
		t.Fatalf("newAgentSession error = %v", err)
	}
	if backendInstance.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backendInstance.closeCalls)
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
