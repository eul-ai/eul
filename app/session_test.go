package app

import (
	"context"
	"encoding/json"
	"errors"
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

func TestModelSetSelectsSubagentProfiles(t *testing.T) {
	models := modelSet{
		main:     "main-model",
		balanced: "balanced-model",
		fast:     "fast-model",
	}
	for _, test := range []struct {
		profile subagent.Profile
		want    string
	}{
		{profile: subagent.ProfileFast, want: "fast-model"},
		{profile: subagent.ProfileBalanced, want: "balanced-model"},
		{profile: subagent.ProfileMain, want: "main-model"},
	} {
		if got := models.forProfile(test.profile); got != test.want {
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
	monthlyUsage := 12.34
	limitRemaining := 87.66
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
			return backend.AccountUsage{
				Windows:           []backend.UsageWindow{{UsedPercent: 25}},
				MonthlyUsageUSD:   &monthlyUsage,
				LimitRemainingUSD: &limitRemaining,
			}, nil
		},
	}
	cwd := t.TempDir()
	skills := []skill.Skill{{Name: "review", Description: "Review code"}}
	warnings := []string{"Skipped skill invalid: malformed"}
	session, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, thinkingLevel: agent.ThinkingMedium, cwd: cwd, skills: skills, warnings: warnings}, environment{}, backendRuntime)
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
	if usageCalls != 1 || len(usage.Windows) != 1 || usage.MonthlyUsageUSD == nil || *usage.MonthlyUsageUSD != monthlyUsage || usage.LimitRemainingUSD == nil || *usage.LimitRemainingUSD != limitRemaining {
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

func TestNewAgentSessionWiresSubagentInstructionsExplicitly(t *testing.T) {
	childStarted := make(chan struct{})
	releaseChild := make(chan struct{})
	var parentRequest agent.Request
	backendRuntime := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			if len(request.Inputs) == 1 && request.Inputs[0].PlainText() == "child prompt" {
				close(childStarted)
				<-releaseChild
				return agent.Response{Text: "child result"}, nil
			}
			parentRequest = request
			return agent.Response{Text: "done"}, nil
		}), nil
	}}
	session, err := newAgentSession(resolvedConfig{
		models: modelSet{main: "model", fast: "model", balanced: "model"},
		cwd:    t.TempDir(),
	}, environment{}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(releaseChild)
		if err := session.finish(nil); err != nil {
			t.Error(err)
		}
	}()

	result, err := session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "launch", Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect scheduler","prompt":"child prompt"}]}`),
	}, nil)
	if err != nil || result.IsError {
		t.Fatalf("launch result = %+v, error = %v", result, err)
	}
	select {
	case <-childStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child request")
	}
	status := session.subagents.Snapshot()
	if len(status.Active) != 1 {
		t.Fatalf("active subagents = %+v", status.Active)
	}

	if _, err := session.engine.Run(context.Background(), "inspect parent", func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parentRequest.Instructions, status.Active[0].ID) || !strings.Contains(parentRequest.Instructions, status.Active[0].Task) {
		t.Fatalf("instructions omit active subagent data: %q", parentRequest.Instructions)
	}
}

func TestNewAgentSessionRejectsUnsupportedFastMode(t *testing.T) {
	configured := &fakeBackendRuntime{newProvider: func() (agent.Provider, error) {
		return metadataFreeProvider{}, nil
	}}
	backendRuntime := metadataFreeBackendRuntime{Runtime: configured}
	session, err := newAgentSession(resolvedConfig{
		models:   modelSet{main: "model", fast: "model", balanced: "model"},
		fastMode: true,
		cwd:      t.TempDir(),
	}, environment{}, backendRuntime)
	if session != nil || err == nil {
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
		models:        modelSet{main: "main", fast: "fast", balanced: "balanced"},
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
		ID: "fast", Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"fast","thinking_level":"high"}]}`),
	}, nil)
	if err != nil || !result.IsError {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	result, err = session.tools.Execute(context.Background(), agent.ToolCall{
		ID: "balanced", Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"balanced","thinking_level":"high"}]}`),
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

func TestNewAgentSessionSubagentDefaultsFollowMainSettings(t *testing.T) {
	requests := make(chan agent.Request, 2)
	provider := &profileMetadataProvider{
		providerFunction: func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			requests <- request
			return agent.Response{Text: "done"}, nil
		},
		metadata: map[string]backend.ModelMetadata{
			"main":     {ThinkingLevels: agent.ThinkingLevels()},
			"fast":     {ThinkingLevels: agent.ThinkingLevels()},
			"balanced": {ThinkingLevels: agent.ThinkingLevels()},
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
		models:        modelSet{main: "main", fast: "fast", balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           t.TempDir(),
	}, runtime, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	launch := func(id string) agent.Request {
		result, err := session.tools.Execute(context.Background(), agent.ToolCall{
			ID: id, Name: "subagent", Arguments: json.RawMessage(`{"tasks":[{"description":"inspect","prompt":"inspect"}]}`),
		}, nil)
		if err != nil || result.IsError {
			t.Fatalf("launch result = %+v, error = %v", result, err)
		}
		select {
		case request := <-requests:
			return request
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for subagent request")
			return agent.Request{}
		}
	}

	request := launch("high")
	if request.Model != "main" || request.ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("high request = %+v", request)
	}
	if err := session.engine.SetThinkingLevel(agent.ThinkingMinimal); err != nil {
		t.Fatal(err)
	}
	request = launch("minimal")
	if request.Model != "main" || request.ThinkingLevel != agent.ThinkingMinimal {
		t.Fatalf("minimal request = %+v", request)
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
	session, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, cwd: cwd}, runtime, backendRuntime)
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
	_, err := newAgentSession(resolvedConfig{models: modelSet{main: "model", fast: "model", balanced: "model"}, cwd: t.TempDir()}, runtime, backendRuntime)
	if !errors.Is(err, configureErr) {
		t.Fatalf("newAgentSession error = %v", err)
	}
	if backendRuntime.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backendRuntime.closeCalls)
	}
}
