package compaction

import (
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestShouldCompact(t *testing.T) {
	state := []byte("state")
	image := agent.NewUserInput(agent.ContentPart{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}})
	for _, test := range []struct {
		name          string
		request       agent.Request
		usage         agent.Usage
		contextWindow int64
		stateTooLarge bool
		want          bool
	}{
		{name: "no state", usage: agent.Usage{TotalTokens: 900}, contextWindow: 1_000, stateTooLarge: true},
		{name: "state too large", request: agent.Request{State: state}, stateTooLarge: true, want: true},
		{name: "missing usage", request: agent.Request{State: state}, contextWindow: 1_000},
		{name: "at threshold", request: agent.Request{State: state}, usage: agent.Usage{TotalTokens: 900}, contextWindow: 1_000, want: true},
		{name: "pending text", request: agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput("12345")}}, usage: agent.Usage{TotalTokens: 898}, contextWindow: 1_000, want: true},
		{name: "below threshold", request: agent.Request{State: state, Inputs: []agent.Input{agent.NewTextInput("1234")}}, usage: agent.Usage{TotalTokens: 898}, contextWindow: 1_000},
		{name: "pending image", request: agent.Request{State: state, Inputs: []agent.Input{image}}, usage: agent.Usage{TotalTokens: 100}, contextWindow: 1_248, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldCompact(test.request, test.usage, test.contextWindow, test.stateTooLarge); got != test.want {
				t.Fatalf("ShouldCompact() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPrepareAndValidateSummary(t *testing.T) {
	original := agent.Request{
		Instructions: "original",
		Inputs:       []agent.Input{agent.NewTextInput("pending")},
		Tools:        []agent.ToolDefinition{{Name: "read"}},
	}
	prepared, continueTask := Prepare(original, "summarize")
	if !continueTask || prepared.Instructions != "summarize" || len(prepared.Tools) != 0 || len(prepared.Inputs) != 2 {
		t.Fatalf("prepared request = %+v, continue = %v", prepared, continueTask)
	}
	if original.Instructions != "original" || len(original.Inputs) != 1 || len(original.Tools) != 1 {
		t.Fatalf("original request changed: %+v", original)
	}

	summary, err := ValidateSummary("  result  ", 0)
	if err != nil || summary != "result" || !strings.Contains(FormatSummary(summary), summary) {
		t.Fatalf("summary = %q, error = %v", summary, err)
	}
	if _, err := ValidateSummary("result", 1); err == nil {
		t.Fatal("summary with a tool call was accepted")
	}
	if _, err := ValidateSummary(" ", 0); err == nil {
		t.Fatal("empty summary was accepted")
	}
}
