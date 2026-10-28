package session

import (
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestRestoreTranscriptInterruptsPendingTools(t *testing.T) {
	transcript := RestoreTranscript([]Entry{{Kind: EntryToolPending, ToolCallID: "call", ToolName: "edit"}})
	if len(transcript.Entries) != 1 || transcript.Entries[0].Kind != EntryToolError || transcript.Entries[0].Tool.Outcome != "interrupted" {
		t.Fatalf("restored entries = %+v", transcript.Entries)
	}
}

func TestDescriptionUsesFirstPromptLine(t *testing.T) {
	var transcript Transcript
	transcript.Append(EntryInfo, "notice")
	transcript.AppendUser([]agent.ContentPart{{Kind: agent.ContentPartText, Text: "  First line of the prompt  \nsecond line"}})
	if description := Description(transcript.Entries); description != "First line of the prompt" {
		t.Fatalf("description = %q", description)
	}
}

func TestDescriptionOfImageOnlyPrompt(t *testing.T) {
	var transcript Transcript
	transcript.AppendUser([]agent.ContentPart{{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png"}}})
	if description := Description(transcript.Entries); description == "" {
		t.Fatal("image-only prompt has no description")
	}
}

func TestAppendUserDropsImageData(t *testing.T) {
	var transcript Transcript
	transcript.AppendUser([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "before "},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
	})
	content := transcript.Entries[0].Content
	if len(content) != 2 || content[1].Kind != agent.ContentPartImage || content[1].Image != nil {
		t.Fatalf("content = %+v", content)
	}
}
