package compaction

import (
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

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
