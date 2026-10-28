package codex

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestThinkingLevelMappings(t *testing.T) {
	levels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
		agent.ThinkingXHigh,
		agent.ThinkingMax,
	}
	for _, model := range []string{modelFast, modelBalanced, modelMain} {
		if got := modelMetadata(model).ThinkingLevels; !slices.Equal(got, levels) {
			t.Fatalf("%s levels = %v, want %v", model, got, levels)
		}
		if _, err := reasoningFor(model, agent.ThinkingMinimal); err == nil {
			t.Fatalf("%s accepts minimal thinking", model)
		}
	}
	for _, test := range []struct {
		level       agent.ThinkingLevel
		wantEffort  string
		wantSummary string
	}{
		{level: agent.ThinkingOff, wantEffort: "none"},
		{level: agent.ThinkingLow, wantEffort: "low", wantSummary: "auto"},
		{level: agent.ThinkingMedium, wantEffort: "medium", wantSummary: "auto"},
		{level: agent.ThinkingHigh, wantEffort: "high", wantSummary: "auto"},
		{level: agent.ThinkingXHigh, wantEffort: "xhigh", wantSummary: "auto"},
		{level: agent.ThinkingMax, wantEffort: "max", wantSummary: "auto"},
	} {
		reasoning, err := reasoningFor(modelMain, test.level)
		if err != nil {
			t.Fatalf("reasoningFor(%q) error = %v", test.level, err)
		}
		if reasoning.Effort != test.wantEffort || reasoning.Summary != test.wantSummary {
			t.Fatalf("reasoningFor(%q) = %+v", test.level, reasoning)
		}
	}

	off, err := reasoningFor(modelMain, agent.ThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(off)
	if err != nil || string(encoded) != `{"effort":"none"}` {
		t.Fatalf("off reasoning = %s, %v", encoded, err)
	}
}
