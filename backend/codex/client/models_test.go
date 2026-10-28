package client

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
)

func TestNewRejectsNilTokenSource(t *testing.T) {
	client, err := New(nil)
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
	if !MetadataFor(ModelGPT56Sol).FastMode {
		t.Fatalf("%s does not support fast mode", ModelGPT56Sol)
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
		if _, err := responseReasoningFor(model, agent.ThinkingMinimal); err == nil {
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
		reasoning, err := responseReasoningFor(ModelGPT56Sol, test.level)
		if err != nil {
			t.Fatalf("responseReasoningFor(%q) error = %v", test.level, err)
		}
		if reasoning.Effort != test.wantEffort || reasoning.Summary != test.wantSummary {
			t.Fatalf("responseReasoningFor(%q) = %+v", test.level, reasoning)
		}
	}

	off, err := responseReasoningFor(ModelGPT56Sol, agent.ThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(off)
	if err != nil || string(encoded) != `{"effort":"none"}` {
		t.Fatalf("off reasoning = %s, %v", encoded, err)
	}
}

func TestClientShouldCompact(t *testing.T) {
	responsesClient := responses.New(responses.Options{Endpoint: "https://example.com/responses"})
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
