package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
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
	_, err := testLauncher(t, t.TempDir(), testBackendRuntime(emptyProvider())).start(context.Background(), sessionSettings{
		models: backend.Models{Main: "unknown", Fast: "model", Balanced: "model"},
		cwd:    t.TempDir(),
	}, "")
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
	writeConfigTestSkill(t, filepath.Join(cwd, ".agents", "skills", "review", "SKILL.md"), "review", "Review code")
	brokenSkill := filepath.Join(cwd, ".agents", "skills", "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(brokenSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenSkill, []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := startTestSession(t, testLauncher(t, cwd, backendRuntime), sessionSettings{models: backend.Models{Main: "model", Fast: "model", Balanced: "model"}, thinkingLevel: agent.ThinkingMedium, cwd: cwd})
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
	if warnings := session.terminalOptions.Config.Warnings; len(warnings) != 1 || !strings.Contains(warnings[0], filepath.ToSlash(brokenSkill)) {
		t.Fatalf("terminal warnings = %v", warnings)
	}
}

func TestNewAgentSessionWiresSubagentInstructionsExplicitly(t *testing.T) {
	childStarted := make(chan struct{})
	releaseChild := make(chan struct{})
	caller := &toolCaller{child: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		close(childStarted)
		<-releaseChild
		return agent.Response{Text: "child result"}, nil
	}}
	cwd := t.TempDir()
	session := startTestSession(t, testLauncher(t, cwd, caller.runtime()), sessionSettings{models: backend.Models{Main: "model"}, cwd: cwd})
	defer func() {
		close(releaseChild)
		if err := session.finish(nil); err != nil {
			t.Error(err)
		}
	}()

	result, err := caller.call(session, "launch_subagents", `{"tasks":[{"description":"inspect scheduler","prompt":"child prompt"}]}`)
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
	if !strings.Contains(caller.request.Instructions, status.Active[0].ID) || !strings.Contains(caller.request.Instructions, status.Active[0].Description) {
		t.Fatalf("instructions omit active subagent data: %q", caller.request.Instructions)
	}
}

func TestNewAgentSessionRejectsUnsupportedFastMode(t *testing.T) {
	backendRuntime := &backend.Runtime{Provider: emptyProvider(), Catalog: map[string]backend.ModelMetadata{"model": {}}}
	session, err := testLauncher(t, t.TempDir(), backendRuntime).start(context.Background(), sessionSettings{
		models:   backend.Models{Main: "model", Fast: "model", Balanced: "model"},
		fastMode: true,
		cwd:      t.TempDir(),
	}, "")
	if session != nil || err == nil {
		t.Fatalf("session=%v error=%v", session, err)
	}
}

func TestNewAgentSessionUsesMetadataForEachModelProfile(t *testing.T) {
	caller := &toolCaller{child: func(context.Context, agent.Request, agent.TextSink) (agent.Response, error) {
		return agent.Response{Text: "done"}, nil
	}}
	backendRuntime := caller.runtime()
	backendRuntime.Catalog = map[string]backend.ModelMetadata{
		"main":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
		"fast":     {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}},
		"balanced": {ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingHigh}},
	}
	cwd := t.TempDir()
	session := startTestSession(t, testLauncher(t, cwd, backendRuntime), sessionSettings{
		models:        backend.Models{Main: "main", Fast: "fast", Balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           cwd,
	})

	result, err := caller.call(session, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"fast","thinking_level":"high"}]}`)
	if err != nil || !result.IsError {
		t.Fatalf("fast result = %+v, error = %v", result, err)
	}
	result, err = caller.call(session, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect","model_profile":"balanced","thinking_level":"high"}]}`)
	if err != nil || result.IsError {
		t.Fatalf("balanced result = %+v, error = %v", result, err)
	}
	if err := session.finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewAgentSessionSubagentDefaultsFollowMainSettings(t *testing.T) {
	requests := make(chan agent.Request, 2)
	caller := &toolCaller{child: func(_ context.Context, request agent.Request, _ agent.TextSink) (agent.Response, error) {
		requests <- request
		return agent.Response{Text: "done"}, nil
	}}
	backendRuntime := caller.runtime()
	backendRuntime.Catalog = testCatalog("main", "fast", "balanced")
	cwd := t.TempDir()
	session := startTestSession(t, testLauncher(t, cwd, backendRuntime), sessionSettings{
		models:        backend.Models{Main: "main", Fast: "fast", Balanced: "balanced"},
		thinkingLevel: agent.ThinkingHigh,
		cwd:           cwd,
	})
	defer session.finish(nil)

	launch := func() agent.Request {
		result, err := caller.call(session, "launch_subagents", `{"tasks":[{"description":"inspect","prompt":"inspect"}]}`)
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
	if err := session.agentSettings.SetThinkingLevel(agent.ThinkingMinimal); err != nil {
		t.Fatal(err)
	}
	request = launch()
	if request.Model != "main" || request.ThinkingLevel != agent.ThinkingMinimal {
		t.Fatalf("minimal request = %+v", request)
	}
}

func TestNewAgentSessionWiresUpdateGoalToEngine(t *testing.T) {
	caller := &toolCaller{}
	cwd := t.TempDir()
	session := startTestSession(t, testLauncher(t, cwd, caller.runtime()), sessionSettings{models: backend.Models{Main: "model"}, cwd: cwd})
	defer session.finish(nil)

	result, err := caller.call(session, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("inactive completion result = %+v", result)
	}
	if err := session.terminalOptions.Controls.SetGoal("finish"); err != nil {
		t.Fatal(err)
	}

	result, err = caller.call(session, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatal(err)
	}
	goal, ok := session.terminalOptions.Controls.Goal()
	if result.IsError || !ok || !goal.Complete {
		t.Fatalf("result=%+v goal=%+v exists=%v", result, goal, ok)
	}
}
