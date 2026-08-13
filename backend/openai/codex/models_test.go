package codex

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestClientReasoningSummaryAndModelMetadata(t *testing.T) {
	client, err := New(testTokenSource("token"), Options{ReasoningSummary: ReasoningSummaryDetailed})
	if err != nil {
		t.Fatal(err)
	}
	if client.reasoningSummary != ReasoningSummaryDetailed {
		t.Fatalf("reasoning summary = %q", client.reasoningSummary)
	}
	defaultClient, err := New(testTokenSource("token"), Options{})
	if err != nil || defaultClient.reasoningSummary != ReasoningSummaryAuto {
		t.Fatalf("default reasoning summary = %q, err = %v", defaultClient.reasoningSummary, err)
	}
	metadata := client.ModelMetadata("unknown")
	if metadata.ContextWindow != 0 || metadata.FastMode || metadata.ClampThinkingLevel(agent.ThinkingXHigh) != agent.ThinkingHigh {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata := client.ModelMetadata("gpt-5.6-sol"); !metadata.FastMode {
		t.Fatalf("gpt-5.6-sol metadata = %+v", metadata)
	}

	if _, err := New(testTokenSource("token"), Options{ReasoningSummary: ReasoningSummary("verbose")}); err == nil {
		t.Fatal("invalid reasoning summary option was accepted")
	}
}

func TestNewRejectsNilTokenSource(t *testing.T) {
	client, err := New(nil, Options{})
	if client != nil || err == nil || !strings.Contains(err.Error(), "token source is required") {
		t.Fatalf("client = %v, error = %v", client, err)
	}
}

func TestContextWindow(t *testing.T) {
	if got := contextWindow("gpt-5.6-sol"); got != 272_000 {
		t.Fatalf("contextWindow() = %d", got)
	}
	if got := contextWindow("unknown"); got != 0 {
		t.Fatalf("contextWindow(unknown) = %d", got)
	}
}

func TestThinkingLevelMappings(t *testing.T) {
	allLevels := agent.ThinkingLevels()
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol"} {
		if got := supportedThinkingLevels(model); !slices.Equal(got, allLevels) {
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
	if got := supportedThinkingLevels("unknown"); !slices.Equal(got, standardLevels) {
		t.Fatalf("unknown model levels = %v, want %v", got, standardLevels)
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
		reasoning, err := responseReasoningFor("gpt-5.6-sol", test.level, ReasoningSummaryAuto)
		if err != nil {
			t.Fatalf("responseReasoningFor(%q) error = %v", test.level, err)
		}
		if reasoning.Effort != test.wantEffort || reasoning.Summary != test.wantSummary {
			t.Fatalf("responseReasoningFor(%q) = %+v", test.level, reasoning)
		}
	}

	unknown, err := responseReasoningFor("unknown", agent.ThinkingHigh, ReasoningSummaryAuto)
	if err != nil || unknown.Effort != "high" {
		t.Fatalf("unknown model high reasoning = %+v, %v", unknown, err)
	}

	off, err := responseReasoningFor("gpt-5.6-sol", agent.ThinkingOff, ReasoningSummaryDetailed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(off)
	if err != nil || string(encoded) != `{"effort":"none"}` {
		t.Fatalf("off reasoning = %s, %v", encoded, err)
	}

	for _, test := range []struct {
		value ReasoningSummary
		want  string
	}{
		{value: ReasoningSummaryAuto, want: "auto"},
		{value: ReasoningSummaryConcise, want: "concise"},
		{value: ReasoningSummaryDetailed, want: "detailed"},
		{value: ReasoningSummaryNone},
	} {
		reasoning, err := responseReasoningFor("gpt-5.6-sol", agent.ThinkingHigh, test.value)
		if err != nil {
			t.Fatal(err)
		}
		if reasoning.Summary != test.want {
			t.Fatalf("summary %q = %q, want %q", test.value, reasoning.Summary, test.want)
		}
	}
}

func TestParseReasoningSummary(t *testing.T) {
	for _, value := range []string{"", "auto", "concise", "detailed", "none"} {
		if _, err := ParseReasoningSummary(value); err != nil {
			t.Fatalf("ParseReasoningSummary(%q): %v", value, err)
		}
	}
	if _, err := ParseReasoningSummary("verbose"); err == nil {
		t.Fatal("invalid reasoning summary was accepted")
	}
}

func TestEstimateInputTokensUsesOrderedTextParts(t *testing.T) {
	inputs := []agent.Input{{
		Text: "ignored",
		Content: &agent.Content{Parts: []agent.ContentPart{
			{Kind: agent.ContentPartText, Text: "12345"},
			{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("image")}},
			{Kind: agent.ContentPartText, Text: "678"},
		}},
	}}
	if got := estimateInputTokens(inputs); got != 2 {
		t.Fatalf("tokens = %d, want 2", got)
	}
}

func TestClientShouldCompactForContinuationStateHeadroom(t *testing.T) {
	client := &Client{maxStateBytes: 160, stateOutputHeadroom: 50}
	state, err := encodeState(nil, nil, []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + strings.Repeat("x", 35) + `"}`)}, client.maxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{State: state, Inputs: []agent.Input{{Kind: agent.InputUser, Text: strings.Repeat("y", 25)}}}

	if !client.ShouldCompact(request, agent.Usage{}) {
		t.Fatal("near-limit state did not trigger compaction")
	}
	request.State = nil
	if client.ShouldCompact(request, agent.Usage{}) {
		t.Fatal("fresh input triggered compaction without state")
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
