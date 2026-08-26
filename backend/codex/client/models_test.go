package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
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
	unknown := MetadataFor("unknown")
	if unknown.ContextWindow != 0 || unknown.FastMode || !slices.Equal(unknown.ThinkingLevels, []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh}) {
		t.Fatal("unknown model metadata is invalid")
	}
	if !MetadataFor(ModelGPT56Sol).FastMode {
		t.Fatalf("%s does not support fast mode", ModelGPT56Sol)
	}

	if _, err := New(testTokenSource("token"), Options{ReasoningSummary: ReasoningSummary("verbose")}); err == nil {
		t.Fatal("invalid reasoning summary option was accepted")
	}
}

func TestNewRejectsNilTokenSource(t *testing.T) {
	client, err := New(nil, Options{})
	if client != nil || err == nil {
		t.Fatalf("client = %v, error = %v", client, err)
	}
}

func TestModelMetadata(t *testing.T) {
	if got := MetadataFor(ModelGPT56Sol).ContextWindow; got != 272_000 {
		t.Fatalf("context window = %d", got)
	}
	if got := MetadataFor("unknown").ContextWindow; got != 0 {
		t.Fatalf("unknown context window = %d", got)
	}
}

func TestThinkingLevelMappings(t *testing.T) {
	gpt56Levels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
		agent.ThinkingXHigh,
		agent.ThinkingMax,
	}
	for _, model := range []string{ModelGPT56Luna, ModelGPT56Terra, ModelGPT56Sol} {
		if got := MetadataFor(model).ThinkingLevels; !slices.Equal(got, gpt56Levels) {
			t.Fatalf("%s levels = %v, want %v", model, got, gpt56Levels)
		}
		if _, err := responseReasoningFor(model, agent.ThinkingMinimal, ReasoningSummaryAuto); err == nil {
			t.Fatalf("%s accepts minimal thinking", model)
		}
	}
	standardLevels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
	}
	if got := MetadataFor("unknown").ThinkingLevels; !slices.Equal(got, standardLevels) {
		t.Fatalf("unknown model levels = %v, want %v", got, standardLevels)
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
		reasoning, err := responseReasoningFor(ModelGPT56Sol, test.level, ReasoningSummaryAuto)
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

	off, err := responseReasoningFor(ModelGPT56Sol, agent.ThinkingOff, ReasoningSummaryDetailed)
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
		reasoning, err := responseReasoningFor(ModelGPT56Sol, agent.ThinkingHigh, test.value)
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

func TestClientShouldCompactForContinuationStateHeadroom(t *testing.T) {
	responsesClient, err := responses.New(responses.Options{
		Endpoint:            "https://example.com/responses",
		MaxStateBytes:       160,
		StateOutputHeadroom: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{responses: responsesClient}
	state, err := json.Marshal(map[string]any{"version": 1, "items": []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"` + strings.Repeat("x", 35) + `"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput(strings.Repeat("y", 25))}}

	if !client.ShouldCompact(request, agent.Usage{}) {
		t.Fatal("near-limit state did not trigger compaction")
	}
	request.State = nil
	if client.ShouldCompact(request, agent.Usage{}) {
		t.Fatal("fresh input triggered compaction without state")
	}
}

func TestClientShouldCompact(t *testing.T) {
	responsesClient, err := responses.New(responses.Options{Endpoint: "https://example.com/responses"})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{responses: responsesClient}
	state := []byte(`{"version":1}`)
	solLimit := models[ModelGPT56Sol].contextWindow * 9 / 10
	terraLimit := models[ModelGPT56Terra].contextWindow * 9 / 10
	lunaLimit := models[ModelGPT56Luna].contextWindow * 9 / 10
	tests := []struct {
		name    string
		request agent.Request
		usage   agent.Usage
		want    bool
	}{
		{name: "no state", request: agent.Request{Model: ModelGPT56Sol}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "no usage", request: agent.Request{Model: ModelGPT56Sol, State: state}, want: false},
		{name: "unknown model", request: agent.Request{Model: "unknown", State: state}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "below limit", request: agent.Request{Model: ModelGPT56Sol, State: state}, usage: agent.Usage{TotalTokens: solLimit - 1}, want: false},
		{name: "sol at limit", request: agent.Request{Model: ModelGPT56Sol, State: state}, usage: agent.Usage{TotalTokens: solLimit}, want: true},
		{name: "terra at limit", request: agent.Request{Model: ModelGPT56Terra, State: state}, usage: agent.Usage{TotalTokens: terraLimit}, want: true},
		{name: "pending input crosses luna limit", request: agent.Request{Model: ModelGPT56Luna, State: state, Inputs: []agent.Input{agent.NewTextInput("12345678")}}, usage: agent.Usage{TotalTokens: lunaLimit - 2}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ShouldCompact(test.request, test.usage); got != test.want {
				t.Fatalf("ShouldCompact() = %v, want %v", got, test.want)
			}
		})
	}
}
