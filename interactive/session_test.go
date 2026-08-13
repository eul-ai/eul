package interactive

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
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/tool"
)

type closeRecordingTool struct {
	closeErr error
	closed   int
}

type closeRecordingToolSet struct {
	tool *closeRecordingTool
}

func (set closeRecordingToolSet) Tools() []tool.Tool { return []tool.Tool{set.tool} }
func (set closeRecordingToolSet) Close() error {
	return set.tool.Close()
}

type metadataFreeProvider struct{}

func (metadataFreeProvider) Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error) {
	return agent.Response{}, nil
}

type profileMetadataProvider struct {
	providerFunction
	metadata  map[string]backend.ModelMetadata
	requested []string
}

func (provider *profileMetadataProvider) metadataFor(model string) backend.ModelMetadata {
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

func TestModelSelectionSelectsSubagentProfiles(t *testing.T) {
	models := modelSelection{
		main:     "powerful-model",
		balanced: "balanced-model",
		fast:     "fast-model",
	}
	for _, test := range []struct {
		profile subagent.Profile
		want    string
	}{
		{profile: subagent.ProfileFast, want: "fast-model"},
		{profile: subagent.ProfileBalanced, want: "balanced-model"},
		{profile: subagent.ProfilePowerful, want: "powerful-model"},
	} {
		if got := models.subagent(test.profile); got != test.want {
			t.Fatalf("profile %q selected %q, want %q", test.profile, got, test.want)
		}
	}
}

func TestRuntimeModelMetadataDefaultsToThinkingOff(t *testing.T) {
	metadata := runtimeModelMetadata(metadataFreeBackendRuntime{}, "model")
	if !slices.Equal(metadata.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff}) {
		t.Fatalf("thinking levels = %v", metadata.ThinkingLevels)
	}
}

func TestNewAgentSessionWiresRuntimeUsage(t *testing.T) {
	usageCalls := 0
	backendRuntime := &fakeBackendRuntime{
		newProvider: func() (agent.Provider, error) {
			return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
				return agent.Response{}, nil
			}), nil
		},
		metadata: func(string) backend.ModelMetadata {
			return backend.ModelMetadata{
				ContextWindow:  123_000,
				ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
				FastMode:       true,
			}
		},
		usage: func(context.Context) (backend.AccountUsage, error) {
			usageCalls++
			return backend.AccountUsage{Windows: []backend.UsageWindow{{UsedPercent: 25}}}, nil
		},
	}
	cwd := t.TempDir()
	writeTestLSPConfig(t, cwd)
	skills := []skill.Skill{{Name: "review", Description: "Review code"}}
	warnings := []string{"Skipped skill invalid: malformed"}
	session, err := newAgentSession(resolvedConfig{models: modelSelection{main: "model"}, thinkingLevel: agent.ThinkingMedium, cwd: cwd, skills: skills, warnings: warnings}, environment{}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
	if session.terminalOptions.Services.LoadUsage == nil {
		t.Fatal("backend usage was not wired to the terminal")
	}
	usage, err := session.terminalOptions.Services.LoadUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usageCalls != 1 || len(usage.Windows) != 1 {
		t.Fatalf("usage calls=%d result=%+v", usageCalls, usage)
	}
	if session.terminalOptions.Events.SubagentUpdates == nil {
		t.Fatal("subagent status was not wired to the terminal")
	}
	if session.terminalOptions.Config.WorkingDirectory != cwd {
		t.Fatalf("terminal working directory = %q, want %q", session.terminalOptions.Config.WorkingDirectory, cwd)
	}
	if session.terminalOptions.Config.ContextWindow != 123_000 || session.terminalOptions.Config.ThinkingLevel != agent.ThinkingHigh || !session.terminalOptions.Config.FastModeAvailable {
		t.Fatalf("terminal metadata = %+v", session.terminalOptions)
	}
	if len(session.terminalOptions.Config.Skills) != 1 || session.terminalOptions.Config.Skills[0].Name != "review" {
		t.Fatalf("terminal skills = %+v", session.terminalOptions.Config.Skills)
	}
	if !slices.Equal(session.terminalOptions.Config.Warnings, warnings) {
		t.Fatalf("terminal warnings = %v", session.terminalOptions.Config.Warnings)
	}
}

func TestNewAgentSessionRejectsUnsupportedFastMode(t *testing.T) {
	configured := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return metadataFreeProvider{}, nil
	}}
	backendRuntime := metadataFreeBackendRuntime{Runtime: configured}
	session, err := newAgentSession(resolvedConfig{
		models:   modelSelection{main: "model"},
		fastMode: true,
		cwd:      t.TempDir(),
	}, environment{}, backendRuntime)
	if session != nil || err == nil || !strings.Contains(err.Error(), "fast mode is unavailable") {
		t.Fatalf("session=%v error=%v", session, err)
	}
	if configured.closeCalls != 1 {
		t.Fatalf("backend close calls = %d", configured.closeCalls)
	}
}

func TestNewAgentSessionUsesMetadataForEachModelProfile(t *testing.T) {
	provider := &profileMetadataProvider{
		providerFunction: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{Text: "done"}, nil
		},
		metadata: map[string]backend.ModelMetadata{
			"main":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
			"fast":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}},
			"balanced": {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
		},
	}
	backendRuntime := &fakeBackendRuntime{
		newProvider: func() (agent.Provider, error) {
			return provider, nil
		},
		metadata: provider.metadataFor,
	}
	runtime := environment{newToolset: func(_ string, _ toolAccess, _ bool, _ tool.NetworkAuthorizer, additional ...tool.Tool) (*tool.Registry, error) {
		return tool.NewRegistry(additional)
	}}
	session, err := newAgentSession(resolvedConfig{
		models:        modelSelection{main: "main", fast: "fast", balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           t.TempDir(),
	}, runtime, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(provider.requested, []string{"main", "fast", "balanced"}) {
		t.Fatalf("metadata requests = %v", provider.requested)
	}

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "fast", Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}],"model_profile":"fast","thinking_level":"high"}`),
	}, nil)
	if err != nil || !result.IsError || !strings.Contains(result.Output, "fast model") {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	result, err = session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "balanced", Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}],"model_profile":"balanced","thinking_level":"high"}`),
	}, nil)
	if err != nil || result.IsError {
		t.Fatalf("balanced result = %+v, error = %v", result, err)
	}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
	if backendRuntime.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backendRuntime.closeCalls)
	}
}

func TestNewAgentSessionWiresUpdateGoalToEngine(t *testing.T) {
	runtime := environment{}
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	cwd := t.TempDir()
	writeTestLSPConfig(t, cwd)
	session, err := newAgentSession(resolvedConfig{models: modelSelection{main: "model"}, cwd: cwd}, runtime, backendRuntime)
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
	config := resolvedConfig{provider: "test", models: modelSelection{main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	store := newSessionStore(t.TempDir())

	first, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.engine.Run(context.Background(), "first prompt", func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := sessionStoreTestTerminalCheckpoint(t, "first prompt")
	if err := first.terminalOptions.Checkpoints.Save(terminalCheckpoint, false); err != nil {
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
	second, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, handle)
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

	if len(requests) != 2 || string(requests[1].State) != "saved-state" || len(requests[1].Inputs) != 1 || requests[1].Inputs[0].Text != "next prompt" {
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
			return agent.Response{Text: "result for " + request.Inputs[0].Text}, nil
		}), nil
	}}
	config := resolvedConfig{provider: "test", models: modelSelection{main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	store := newSessionStore(t.TempDir())
	session, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)
	if err := session.terminalOptions.Checkpoints.Save(sessionStoreTestTerminalCheckpoint(t, "prompt"), false); err != nil {
		t.Fatal(err)
	}
	launch := tool.NewSubagent(session.subagents)
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
	config := resolvedConfig{provider: "test", models: modelSelection{main: "model"}, thinkingLevel: agent.ThinkingHigh, cwd: cwd}
	store := newSessionStore(t.TempDir())

	first, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	launch := tool.NewSubagent(first.subagents)
	if _, err := launch.Execute(context.Background(), json.RawMessage(`{"tasks":[{"description":"running","prompt":"running"}]}`), nil); err != nil {
		t.Fatal(err)
	}
	terminalCheckpoint := sessionStoreTestTerminalCheckpoint(t, "active prompt")
	if err := first.terminalOptions.Checkpoints.Save(terminalCheckpoint, true); err != nil {
		t.Fatal(err)
	}
	sessionID := first.persistence.handle.record.ID
	first.persistence.checkpoints.Stop()
	if err := first.subagents.Close(); err != nil {
		t.Fatal(err)
	}
	<-first.persistence.changesDone
	if err := first.tools.Close(); err != nil {
		t.Fatal(err)
	}
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
	second, err := newStoredAgentSession(config, runtime, newBackendRuntime(), store, handle)
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
	store := newSessionStore(t.TempDir())

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
	session, err := newStoredAgentSession(config, runtime, backendRuntime, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.terminalOptions.Checkpoints.Save(sessionStoreTestTerminalCheckpoint(t, "saved prompt"), false); err != nil {
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
	if restored.provider != "alternate" || restored.models != (modelSelection{main: "selected-model", fast: "selected-fast-model", balanced: "selected-balanced-model"}) || selected.Descriptor().ID != "alternate" {
		t.Fatalf("restored=%+v provider=%q", restored, selected.Descriptor().ID)
	}
}

func TestResolveStoredSessionSurfacesSkippedSessionWarnings(t *testing.T) {
	cwd := t.TempDir()
	store := newSessionStore(t.TempDir())
	corrupt, err := store.Create("test", cwd, modelSelection{main: "model", fast: "fast-model", balanced: "balanced-model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "corrupt"), false)
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
	valid, err := store.Create("test", cwd, modelSelection{main: "model"}, agent.ThinkingMedium, sessionStoreTestAgentCheckpoint(t), subagent.EmptyCheckpoint(), sessionStoreTestTerminalCheckpoint(t, "valid"), false)
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
	if len(config.warnings) != 1 || !strings.Contains(config.warnings[0], "Skipped session") || !strings.Contains(config.warnings[0], "unsupported session version") {
		t.Fatalf("warnings = %v", config.warnings)
	}
	if config.models != (modelSelection{main: "model", fast: "gpt-5.6-luna", balanced: "gpt-5.6-terra"}) {
		t.Fatalf("restored default models = %+v", config)
	}
}

func TestNewAgentSessionReportsToolsetConfigurationFailure(t *testing.T) {
	configureErr := errors.New("toolset failed")
	runtime := environment{
		newToolset: func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error) {
			return nil, configureErr
		},
	}

	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{}, nil
		}), nil
	}}
	_, err := newAgentSession(resolvedConfig{models: modelSelection{main: "model"}, cwd: t.TempDir()}, runtime, backendRuntime)
	if !errors.Is(err, configureErr) || !strings.Contains(err.Error(), "configure tools") {
		t.Fatalf("newAgentSession error = %v", err)
	}
	if backendRuntime.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backendRuntime.closeCalls)
	}
}

func TestFinishRegistryClosesToolsAndPreservesRunError(t *testing.T) {
	runErr := context.Canceled
	closeErr := errors.New("close failed")
	closer := &closeRecordingTool{closeErr: closeErr}
	registry, err := tool.NewRegistry(nil, closeRecordingToolSet{tool: closer})
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

func TestAgentSessionFinishClosesOwnedRegistry(t *testing.T) {
	closer := &closeRecordingTool{}
	registry, err := tool.NewRegistry(nil, closeRecordingToolSet{tool: closer})
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
