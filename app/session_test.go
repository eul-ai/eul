package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

func TestModelSetSelectsSubagentProfiles(t *testing.T) {
	models := backend.Models{
		Main:     "main-model",
		Balanced: "balanced-model",
		Fast:     "fast-model",
	}
	for _, test := range []struct {
		profile subagent.Profile
		want    string
	}{
		{profile: subagent.ProfileFast, want: "fast-model"},
		{profile: subagent.ProfileBalanced, want: "balanced-model"},
		{profile: subagent.ProfileMain, want: "main-model"},
	} {
		if got := modelForProfile(models, test.profile); got != test.want {
			t.Fatalf("profile %q selected %q, want %q", test.profile, got, test.want)
		}
	}
}

func TestNewAgentSessionRejectsUnsupportedModel(t *testing.T) {
	_, err := newAgentSession(resolvedConfig{
		models: backend.Models{Main: "unknown", Fast: "model", Balanced: "model"},
		cwd:    t.TempDir(),
	}, environment{}, testBackendRuntime(emptyProvider()))
	var unsupported *backend.ModelNotSupportedError
	if !errors.As(err, &unsupported) || unsupported.Model != "unknown" {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAgentSessionWiresRuntimeUsage(t *testing.T) {
	usageCalls := 0
	monthlyUsage := 12.34
	limitRemaining := 87.66
	backendRuntime := &backend.Runtime{
		Provider: emptyProvider(),
		Catalog: map[string]backend.ModelMetadata{"model": {
			ContextWindow:  123_000,
			ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
			FastMode:       true,
		}},
		Usage: func(context.Context) (backend.AccountUsage, error) {
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
	session, err := newAgentSession(resolvedConfig{models: backend.Models{Main: "model", Fast: "model", Balanced: "model"}, thinkingLevel: agent.ThinkingMedium, cwd: cwd, skills: skills, warnings: warnings}, environment{}, backendRuntime)
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
	if session.terminalOptions.Events.Subagents == nil {
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
	backendRuntime := testBackendRuntime(providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
		if len(request.Inputs) == 1 && request.Inputs[0].PlainText() == "child prompt" {
			close(childStarted)
			<-releaseChild
			return agent.Response{Text: "child result"}, nil
		}
		parentRequest = request
		return agent.Response{Text: "done"}, nil
	}))
	var tools []agent.Tool
	session, err := newAgentSession(resolvedConfig{
		models: backend.Models{Main: "model", Fast: "model", Balanced: "model"},
		cwd:    t.TempDir(),
	}, environment{newToolset: captureTools(&tools, buildToolset)}, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(releaseChild)
		if err := session.finish(nil); err != nil {
			t.Error(err)
		}
	}()

	result, err := executeTool(tools, "launch_subagents", `{"tasks":[{"description":"inspect scheduler","prompt":"child prompt"}]}`)
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

	if _, err := session.engine.Run(context.Background(), []agent.ContentPart{{Kind: agent.ContentPartText, Text: "inspect parent"}}, func(agent.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parentRequest.Instructions, status.Active[0].ID) || !strings.Contains(parentRequest.Instructions, status.Active[0].Description) {
		t.Fatalf("instructions omit active subagent data: %q", parentRequest.Instructions)
	}
}

func TestNewAgentSessionRejectsUnsupportedFastMode(t *testing.T) {
	backendRuntime := &backend.Runtime{Provider: emptyProvider(), Catalog: map[string]backend.ModelMetadata{"model": {}}}
	session, err := newAgentSession(resolvedConfig{
		models:   backend.Models{Main: "model", Fast: "model", Balanced: "model"},
		fastMode: true,
		cwd:      t.TempDir(),
	}, environment{}, backendRuntime)
	if session != nil || err == nil {
		t.Fatalf("session=%v error=%v", session, err)
	}
}

func TestNewAgentSessionUsesMetadataForEachModelProfile(t *testing.T) {
	backendRuntime := &backend.Runtime{
		Provider: providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
			return agent.Response{Text: "done"}, nil
		}),
		Catalog: map[string]backend.ModelMetadata{
			"main":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
			"fast":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}},
			"balanced": {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
		},
	}
	var tools []agent.Tool
	runtime := environment{newToolset: captureTools(&tools, additionalTools)}
	session, err := newAgentSession(resolvedConfig{
		models:        backend.Models{Main: "main", Fast: "fast", Balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           t.TempDir(),
	}, runtime, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}

	result, err := executeTool(tools, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"fast","thinking_level":"high"}]}`)
	if err != nil || !result.IsError {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	result, err = executeTool(tools, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"balanced","thinking_level":"high"}]}`)
	if err != nil || result.IsError {
		t.Fatalf("balanced result = %+v, error = %v", result, err)
	}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewAgentSessionSubagentDefaultsFollowMainSettings(t *testing.T) {
	requests := make(chan agent.Request, 2)
	backendRuntime := &backend.Runtime{
		Provider: providerFunction(func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
			requests <- request
			return agent.Response{Text: "done"}, nil
		}),
		Catalog: testCatalog("main", "fast", "balanced"),
	}
	var tools []agent.Tool
	runtime := environment{newToolset: captureTools(&tools, additionalTools)}
	session, err := newAgentSession(resolvedConfig{
		models:        backend.Models{Main: "main", Fast: "fast", Balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           t.TempDir(),
	}, runtime, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	launch := func() agent.Request {
		result, err := executeTool(tools, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect"}]}`)
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

	request := launch()
	if request.Model != "main" || request.ThinkingLevel != agent.ThinkingHigh {
		t.Fatalf("high request = %+v", request)
	}
	if err := session.settings.SetThinkingLevel(agent.ThinkingMinimal); err != nil {
		t.Fatal(err)
	}
	request = launch()
	if request.Model != "main" || request.ThinkingLevel != agent.ThinkingMinimal {
		t.Fatalf("minimal request = %+v", request)
	}
}

func TestNewAgentSessionWiresUpdateGoalToEngine(t *testing.T) {
	var tools []agent.Tool
	runtime := environment{newToolset: captureTools(&tools, buildToolset)}
	backendRuntime := testBackendRuntime(providerFunction(func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{}, nil
	}))
	cwd := t.TempDir()
	session, err := newAgentSession(resolvedConfig{models: backend.Models{Main: "model", Fast: "model", Balanced: "model"}, cwd: cwd}, runtime, backendRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer session.finish(nil)

	result, err := executeTool(tools, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("inactive completion result = %+v", result)
	}
	if err := session.terminalOptions.Controls.SetGoal("finish"); err != nil {
		t.Fatal(err)
	}

	result, err = executeTool(tools, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatal(err)
	}
	goal, ok := session.terminalOptions.Controls.Goal()
	if result.IsError || !ok || !goal.Complete {
		t.Fatalf("result=%+v goal=%+v exists=%v", result, goal, ok)
	}
}
