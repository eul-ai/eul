package session

import (
	"encoding/json"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestRestoreTranscriptInterruptsPendingTools(t *testing.T) {
	transcript := restoreTranscript([]Entry{{Kind: EntryToolPending, ToolCallID: "call", ToolName: "edit"}})
	if len(transcript.entries) != 1 || transcript.entries[0].Kind != EntryToolError || transcript.entries[0].Tool.Outcome != "interrupted" {
		t.Fatalf("restored entries = %+v", transcript.entries)
	}
}

func TestRestoredTranscriptStartsNewStream(t *testing.T) {
	var transcript Transcript
	transcript.Apply(agent.Event{Kind: agent.EventAssistantText, Text: "first"})
	transcript.Apply(agent.Event{Kind: agent.EventAssistantText, Text: " second"})
	persisted := transcript.persisted()
	transcript.Apply(agent.Event{Kind: agent.EventAssistantText, Text: " third"})
	if persisted[0].Text != "first second" || transcript.entries[0].Text != "first second third" {
		t.Fatalf("persisted = %q, live = %q", persisted[0].Text, transcript.entries[0].Text)
	}

	restored := restoreTranscript(persisted)
	restored.Apply(agent.Event{Kind: agent.EventAssistantText, Text: "new"})
	if len(restored.entries) != 2 || restored.entries[0].Text != "first second" || restored.entries[1].Text != "new" {
		t.Fatalf("restored entries = %+v", restored.entries)
	}
}

func TestDescriptionUsesFirstPromptLine(t *testing.T) {
	var transcript Transcript
	transcript.Append(EntryInfo, "notice")
	transcript.AppendUser([]agent.ContentPart{{Kind: agent.ContentPartText, Text: "  First line of the prompt  \nsecond line"}})
	if description := description(transcript.entries); description != "First line of the prompt" {
		t.Fatalf("description = %q", description)
	}
}

func TestDescriptionOfImageOnlyPrompt(t *testing.T) {
	var transcript Transcript
	transcript.AppendUser([]agent.ContentPart{{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}}})
	if description := description(transcript.entries); description == "" {
		t.Fatal("image-only prompt has no description")
	}
}

func TestUserEntriesKeepImagePositionsWithoutImageData(t *testing.T) {
	var transcript Transcript
	transcript.AppendUser([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: " after"},
	})
	encoded, err := json.Marshal(transcript.persisted())
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Entry
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	content := restoreTranscript(decoded).entries[0].Content
	if len(content) != 3 || content[0].Text != "before " || content[1].Kind != agent.ContentPartImage || content[1].Image != nil || content[2].Text != " after" {
		t.Fatalf("content = %+v", content)
	}
}

func TestSyncCopiesChangedEntries(t *testing.T) {
	var transcript Transcript
	transcript.Apply(agent.Event{Kind: agent.EventToolStart, Call: agent.ToolCall{ID: "call", Name: "read"}})
	transcript.Apply(agent.Event{Kind: agent.EventAssistantText, Text: "answer"})
	copied, changed := transcript.Sync(nil)
	if !changed || len(copied) != 2 {
		t.Fatalf("first sync changed = %v, entries = %+v", changed, copied)
	}
	if _, changed := transcript.Sync(copied); changed {
		t.Fatal("unchanged transcript reported a change")
	}

	transcript.Apply(agent.Event{Kind: agent.EventToolEnd, Call: agent.ToolCall{ID: "call", Name: "read"}, Result: agent.ToolResult{Tool: "read"}})
	transcript.Apply(agent.Event{Kind: agent.EventAssistantText, Text: " continues"})
	copied, changed = transcript.Sync(copied)
	if !changed || len(copied) != 2 || copied[0].Kind != EntryTool || copied[1].Text != "answer continues" {
		t.Fatalf("second sync changed = %v, entries = %+v", changed, copied)
	}
}

func TestFinishTurnForgetsExecutedToolsOfPreviousTurn(t *testing.T) {
	var transcript Transcript
	transcript.Apply(agent.Event{Kind: agent.EventToolExecute, Call: agent.ToolCall{ID: "call", Name: "write"}})
	transcript.FinishTurn(true, nil)
	afterTool := transcript.entries[len(transcript.entries)-1]
	if transcript.entries[0].Kind != EntryToolError || afterTool.Kind != EntryInfo {
		t.Fatalf("entries = %+v", transcript.entries)
	}

	transcript.FinishTurn(true, nil)
	if withoutTool := transcript.entries[len(transcript.entries)-1]; withoutTool.Text == afterTool.Text {
		t.Fatalf("second turn repeated the tool notice: %q", withoutTool.Text)
	}
}
