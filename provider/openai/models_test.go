package openai

import (
	"encoding/json"
	"slices"
	"testing"

	"yaah/agent"
)

func TestContextWindow(t *testing.T) {
	if got := ContextWindow("gpt-5.6-sol"); got != 272_000 {
		t.Fatalf("ContextWindow() = %d", got)
	}
	if got := ContextWindow("unknown"); got != 0 {
		t.Fatalf("ContextWindow(unknown) = %d", got)
	}
}

func TestThinkingLevelMappings(t *testing.T) {
	allLevels := agent.ThinkingLevels()
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol"} {
		if got := SupportedThinkingLevels(model); !slices.Equal(got, allLevels) {
			t.Fatalf("%s levels = %v, want %v", model, got, allLevels)
		}
	}
	standardLevels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
	}
	if got := SupportedThinkingLevels("unknown"); !slices.Equal(got, standardLevels) {
		t.Fatalf("unknown model levels = %v, want %v", got, standardLevels)
	}
	if got := ClampThinkingLevel("unknown", agent.ThinkingXHigh); got != agent.ThinkingHigh {
		t.Fatalf("ClampThinkingLevel() = %q, want high", got)
	}

	for _, test := range []struct {
		level       agent.ThinkingLevel
		wantEffort  string
		wantSummary string
	}{
		{level: agent.ThinkingOff, wantEffort: "none"},
		{level: agent.ThinkingMinimal, wantEffort: "minimal", wantSummary: "auto"},
		{level: agent.ThinkingLow, wantEffort: "low", wantSummary: "auto"},
		{level: agent.ThinkingMedium, wantEffort: "medium", wantSummary: "auto"},
		{level: agent.ThinkingHigh, wantEffort: "high", wantSummary: "auto"},
		{level: agent.ThinkingXHigh, wantEffort: "xhigh", wantSummary: "auto"},
		{level: agent.ThinkingMax, wantEffort: "max", wantSummary: "auto"},
	} {
		reasoning, err := responseReasoningFor("gpt-5.6-sol", test.level)
		if err != nil {
			t.Fatalf("responseReasoningFor(%q) error = %v", test.level, err)
		}
		if reasoning.Effort != test.wantEffort || reasoning.Summary != test.wantSummary {
			t.Fatalf("responseReasoningFor(%q) = %+v", test.level, reasoning)
		}
	}

	unknown, err := responseReasoningFor("unknown", agent.ThinkingHigh)
	if err != nil || unknown.Effort != "high" {
		t.Fatalf("unknown model high reasoning = %+v, %v", unknown, err)
	}

	off, err := responseReasoningFor("gpt-5.6-sol", agent.ThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(off)
	if err != nil || string(encoded) != `{"effort":"none"}` {
		t.Fatalf("off reasoning = %s, %v", encoded, err)
	}
}

func TestClientShouldCompact(t *testing.T) {
	client := &Client{}
	solLimit := models["gpt-5.6-sol"].contextWindow * 9 / 10
	terraLimit := models["gpt-5.6-terra"].contextWindow * 9 / 10
	lunaLimit := models["gpt-5.6-luna"].contextWindow * 9 / 10
	tests := []struct {
		name    string
		request agent.Request
		usage   agent.Usage
		want    bool
	}{
		{name: "no state", request: agent.Request{Model: "gpt-5.6-sol"}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "no usage", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, want: false},
		{name: "unknown model", request: agent.Request{Model: "unknown", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "below limit", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit - 1}, want: false},
		{name: "sol at limit", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit}, want: true},
		{name: "terra at limit", request: agent.Request{Model: "gpt-5.6-terra", State: []byte("state")}, usage: agent.Usage{TotalTokens: terraLimit}, want: true},
		{name: "pending input crosses luna limit", request: agent.Request{Model: "gpt-5.6-luna", State: []byte("state"), Inputs: []agent.Input{{Text: "12345678"}}}, usage: agent.Usage{TotalTokens: lunaLimit - 2}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ShouldCompact(test.request, test.usage); got != test.want {
				t.Fatalf("ShouldCompact() = %v, want %v", got, test.want)
			}
		})
	}
}
