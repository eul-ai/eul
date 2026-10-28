package session

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
	maxToolSummaryBytes = 500
	maxErrorBytes       = 500
	maxDescriptionBytes = 120
)

type EntryKind string

const (
	EntryUser        EntryKind = "user"
	EntryAssistant   EntryKind = "assistant"
	EntryReasoning   EntryKind = "reasoning"
	EntryToolPending EntryKind = "tool_pending"
	EntryTool        EntryKind = "tool"
	EntryToolError   EntryKind = "tool_error"
	EntryContext     EntryKind = "context"
	EntryError       EntryKind = "error"
	EntryInfo        EntryKind = "info"
)

type Entry struct {
	Kind       EntryKind              `json:"kind"`
	Text       string                 `json:"text,omitempty"`
	Content    []agent.ContentPart    `json:"content,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Tool       agent.ToolPresentation `json:"tool,omitzero"`
}

type Transcript struct {
	Entries      []Entry
	stream       strings.Builder
	streamKind   EntryKind
	executedTool bool
}

func RestoreTranscript(entries []Entry) Transcript {
	entries = slices.Clone(entries)
	for index := range entries {
		if entries[index].Kind == EntryToolPending {
			entries[index].Kind = EntryToolError
			entries[index].Tool.Outcome = "interrupted"
		}
	}
	return Transcript{Entries: entries}
}

func (t *Transcript) Apply(event agent.Event) {
	switch event.Kind {
	case agent.EventAssistantReasoning:
		t.appendStream(EntryReasoning, event.Text)
	case agent.EventAssistantText:
		t.appendStream(EntryAssistant, event.Text)
	case agent.EventCompactionEnd:
		t.Append(EntryContext, "Context compacted")
	case agent.EventGoalContinuation:
		t.Append(EntryInfo, "Goal continuing")
	case agent.EventToolStart:
		t.startTool(event.Call, event.Presentation)
	case agent.EventToolUpdate:
		t.updateTool(event.Call, event.Presentation)
	case agent.EventToolExecute:
		t.executedTool = true
		t.updateTool(event.Call, event.Presentation)
	case agent.EventToolEnd:
		t.finishTool(event.Call, event.Presentation, event.Result)
	}
}

func (t *Transcript) Append(kind EntryKind, text string) {
	t.stream.Reset()
	t.Entries = append(t.Entries, Entry{Kind: kind, Text: sanitizeText(text)})
}

func (t *Transcript) AppendUser(content []agent.ContentPart) {
	t.stream.Reset()
	parts := make([]agent.ContentPart, len(content))
	for index, part := range content {
		parts[index] = agent.ContentPart{Kind: part.Kind, Text: sanitizeText(part.Text)}
	}
	t.Entries = append(t.Entries, Entry{Kind: EntryUser, Text: contentText(parts), Content: parts})
}

func (t *Transcript) FinishTurn(interrupted bool, err error) {
	t.stream.Reset()
	executedTool := t.executedTool
	t.executedTool = false

	switch {
	case interrupted:
		t.finishPendingTools("canceled")
		if executedTool {
			t.Append(EntryInfo, "Interrupted; tool side effects may remain")
			return
		}
		t.Append(EntryInfo, "Interrupted")
	case err != nil:
		t.finishPendingTools("failed")
		t.Append(EntryError, singleLine(err.Error(), maxErrorBytes))
		if executedTool {
			t.Append(EntryInfo, "Tool turn interrupted; tool side effects may remain")
		}
	}
}

func (t *Transcript) appendStream(kind EntryKind, text string) {
	text = sanitizeText(text)
	if text == "" {
		return
	}

	if t.stream.Len() > 0 && t.streamKind == kind && len(t.Entries) > 0 {
		t.stream.WriteString(text)
		t.Entries[len(t.Entries)-1].Text = t.stream.String()
		return
	}

	t.stream.Reset()
	t.stream.WriteString(text)
	t.Entries = append(t.Entries, Entry{Kind: kind, Text: t.stream.String()})
	t.streamKind = kind
}

func (t *Transcript) startTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	t.stream.Reset()
	t.Entries = append(t.Entries, Entry{
		Kind:       EntryToolPending,
		ToolCallID: call.ID,
		ToolName:   singleLine(call.Name, maxToolSummaryBytes),
		Tool:       sanitizeToolPresentation(presentation),
	})
}

func (t *Transcript) updateTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	index := t.toolIndex(call.ID)
	if index < 0 {
		t.startTool(call, presentation)
		return
	}
	t.Entries[index].Tool = sanitizeToolPresentation(presentation)
}

func (t *Transcript) finishTool(call agent.ToolCall, presentation agent.ToolPresentation, result agent.ToolResult) {
	kind := EntryTool
	if result.IsError {
		kind = EntryToolError
	}
	index := t.toolIndex(call.ID)
	if index < 0 {
		t.startTool(call, presentation)
		index = len(t.Entries) - 1
	}

	presentation.Outcome = toolResultOutcome(result, presentation)
	t.Entries[index].Kind = kind
	t.Entries[index].Tool = sanitizeToolPresentation(presentation)
}

func (t *Transcript) toolIndex(callID string) int {
	for index := len(t.Entries) - 1; index >= 0; index-- {
		switch t.Entries[index].Kind {
		case EntryToolPending, EntryTool, EntryToolError:
			if t.Entries[index].ToolCallID == callID {
				return index
			}
		}
	}
	return -1
}

func (t *Transcript) finishPendingTools(outcome string) {
	for index := range t.Entries {
		if t.Entries[index].Kind != EntryToolPending {
			continue
		}
		t.Entries[index].Kind = EntryToolError
		t.Entries[index].Tool.Outcome = outcome
	}
}

func Description(entries []Entry) string {
	for _, entry := range entries {
		if entry.Kind != EntryUser {
			continue
		}
		prompt := strings.TrimSpace(entry.Text)
		if prompt == "" && hasImage(entry.Content) {
			return "Image attachment"
		}
		if prompt == "" {
			continue
		}
		line, _, _ := strings.Cut(prompt, "\n")
		return singleLine(strings.TrimSpace(line), maxDescriptionBytes)
	}
	return ""
}

func toolResultOutcome(result agent.ToolResult, presentation agent.ToolPresentation) string {
	if presentation.Outcome != "" {
		return presentation.Outcome
	}
	if !result.IsError {
		return "ok"
	}
	detail, _, _ := strings.Cut(result.Output, "\n")
	if detail == "" {
		return "error"
	}
	return "error: " + detail
}

func sanitizeToolPresentation(presentation agent.ToolPresentation) agent.ToolPresentation {
	presentation.Arguments = singleLine(presentation.Arguments, maxToolSummaryBytes)
	presentation.Outcome = singleLine(presentation.Outcome, maxToolSummaryBytes)
	return presentation
}

func contentText(content []agent.ContentPart) string {
	var text strings.Builder
	for _, part := range content {
		if part.Kind == agent.ContentPartText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func hasImage(content []agent.ContentPart) bool {
	return slices.ContainsFunc(content, func(part agent.ContentPart) bool {
		return part.Kind == agent.ContentPartImage
	})
}

func sanitizeText(value string) string {
	return strings.Map(func(character rune) rune {
		if character != '\n' && character != '\t' && unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func singleLine(value string, maximum int) string {
	value = strings.Join(strings.FieldsFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}), " ")

	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
