package terminal

import (
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/session"
)

func TestStatusTruncatesSessionID(t *testing.T) {
	model := newTestModel(120, 12, &fakeSession{settings: session.Settings{Models: backend.Models{Main: "opaque-model-value"}}, info: session.Info{ID: "0123456789abcdef0123456789abcdef"}})

	_, _, status := renderStatus(model, model.width)
	if !strings.Contains(status, "opaque-model-value") || !strings.Contains(status, string(agent.ThinkingMedium)) || !strings.Contains(status, "01234567") || strings.Contains(status, "0123456789abcdef") {
		t.Fatalf("status = %q", status)
	}
}

func TestStatusShowsProviderUsageWindows(t *testing.T) {
	now := time.Now()
	model := newTestModel(180, 12, &fakeSession{settings: session.Settings{Models: backend.Models{Main: "opaque-model-value"}}})
	model.providerUsage = backend.AccountUsage{Windows: []backend.UsageWindow{
		{Duration: 7 * 24 * time.Hour, UsedPercent: 20, ResetsAt: now.Add(3*24*time.Hour + 5*time.Hour)},
		{Duration: 5 * time.Hour, UsedPercent: 42, ResetsAt: now.Add(3*time.Hour + 5*time.Minute)},
	}}

	_, _, wide := renderStatus(model, 180)
	for _, want := range []string{"opaque-model-value", string(agent.ThinkingMedium), "5h", "42%", "3h 5m", "7d", "20%", "3d 5h"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide status %q omits %q", wide, want)
		}
	}
	if strings.Index(wide, "42%") > strings.Index(wide, "20%") {
		t.Fatalf("usage windows are out of order: %q", wide)
	}

	_, _, narrow := renderStatus(model, 70)
	for _, want := range []string{"5h", "42%", "3h 5m", "7d", "20%", "3d 5h"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow status %q omits %q", narrow, want)
		}
	}
}

func TestStatusUsesCompactContextAndSingleUsage(t *testing.T) {
	now := time.Now()
	model := newTestModel(120, 12, &fakeSession{settings: session.Settings{Models: backend.Models{Main: "gpt-5.6-sol"}, ThinkingLevel: agent.ThinkingXHigh}, info: session.Info{Model: backend.ModelMetadata{ContextWindow: 272_000}}})
	model.providerUsage = backend.AccountUsage{Windows: []backend.UsageWindow{{
		Duration: 7 * 24 * time.Hour, UsedPercent: 59, ResetsAt: now.Add(9*time.Hour + 41*time.Minute),
	}}}

	_, _, status := renderStatus(model, 120)
	for _, want := range []string{"gpt-5.6-sol", string(agent.ThinkingXHigh), "0%", "59%", "9h 41m"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q omits %q", status, want)
		}
	}
}

func TestStatusShowsMonthlyUsageAndLimitRemaining(t *testing.T) {
	monthlyUsage := 12.34
	limitRemaining := 87.66
	usage := backend.AccountUsage{
		MonthlyUsageUSD:   &monthlyUsage,
		LimitRemainingUSD: &limitRemaining,
	}

	long, short := providerUsageText(usage, time.Time{})
	for _, text := range []string{long, short} {
		if !strings.Contains(text, "$12.34") || !strings.Contains(text, "$87.66") {
			t.Fatalf("provider usage = %q", text)
		}
	}

	usage.LimitRemainingUSD = nil
	long, short = providerUsageText(usage, time.Time{})
	for _, text := range []string{long, short} {
		if !strings.Contains(text, "$12.34") || strings.Contains(text, "$87.66") {
			t.Fatalf("unlimited provider usage = %q", text)
		}
	}
}

func TestResetCountdownUsesTwoLargestUnits(t *testing.T) {
	now := time.Date(2027, time.January, 2, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		remaining time.Duration
		want      string
	}{
		{remaining: 3*24*time.Hour + 5*time.Hour + 20*time.Minute, want: "3d 5h"},
		{remaining: 5*time.Hour + 12*time.Minute, want: "5h 12m"},
		{remaining: 30 * time.Second, want: "1m"},
	}
	for _, test := range tests {
		if got := resetCountdown(now.Add(test.remaining), now); got != test.want {
			t.Fatalf("resetCountdown(%s) = %q, want %q", test.remaining, got, test.want)
		}
	}
}
