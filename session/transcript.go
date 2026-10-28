package session

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
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
	EntryNotice      EntryKind = "notice"
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
	entries      []Entry
	synced       int
	stream       strings.Builder
	streamKind   EntryKind
	executedTool bool
}

func restoreTranscript(entries []Entry) Transcript {
	entries = slices.Clone(entries)
	for index := range entries {
		if entries[index].Kind == EntryToolPending {
			entries[index].Kind = EntryToolError
			entries[index].Tool.Outcome = "interrupted"
		}
	}
	return Transcript{entries: entries}
}

// Sync brings a copy of the entries up to date with the transcript. It
// copies only the entries changed since the previous call, so it serves a
// single copy.
func (t *Transcript) Sync(entries []Entry) ([]Entry, bool) {
	if t.synced == len(t.entries) {
		return entries, false
	}
	entries = append(entries[:t.synced], t.entries[t.synced:]...)
	t.synced = len(t.entries)
	return entries, true
}

func (t *Transcript) changed(index int) {
	t.synced = min(t.synced, index)
}

func (t *Transcript) persisted() []Entry {
	return slices.DeleteFunc(slices.Clone(t.entries), func(entry Entry) bool {
		return entry.Kind == EntryNotice
	})
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
	case agent.EventSteering:
		t.AppendUser(event.Content)
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
	t.entries = append(t.entries, Entry{Kind: kind, Text: text})
}

func (t *Transcript) AppendUser(content []agent.ContentPart) {
	t.stream.Reset()
	parts := make([]agent.ContentPart, len(content))
	for index, part := range content {
		parts[index] = agent.ContentPart{Kind: part.Kind, Text: part.Text}
	}
	t.entries = append(t.entries, Entry{Kind: EntryUser, Text: contentText(parts), Content: parts})
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
	if text == "" {
		return
	}

	if t.stream.Len() > 0 && t.streamKind == kind && len(t.entries) > 0 {
		last := len(t.entries) - 1
		t.stream.WriteString(text)
		t.entries[last].Text = t.stream.String()
		t.changed(last)
		return
	}

	t.stream.Reset()
	t.stream.WriteString(text)
	t.entries = append(t.entries, Entry{Kind: kind, Text: t.stream.String()})
	t.streamKind = kind
}

func (t *Transcript) startTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	t.stream.Reset()
	t.entries = append(t.entries, Entry{
		Kind:       EntryToolPending,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Tool:       presentation,
	})
}

func (t *Transcript) updateTool(call agent.ToolCall, presentation agent.ToolPresentation) {
	index := t.toolIndex(call.ID)
	if index < 0 {
		t.startTool(call, presentation)
		return
	}
	t.entries[index].Tool = presentation
	t.changed(index)
}

func (t *Transcript) finishTool(call agent.ToolCall, presentation agent.ToolPresentation, result agent.ToolResult) {
	kind := EntryTool
	if result.IsError {
		kind = EntryToolError
	}
	index := t.toolIndex(call.ID)
	if index < 0 {
		t.startTool(call, presentation)
		index = len(t.entries) - 1
	}

	presentation.Outcome = toolResultOutcome(result, presentation)
	t.entries[index].Kind = kind
	t.entries[index].Tool = presentation
	t.changed(index)
}

func (t *Transcript) toolIndex(callID string) int {
	for index := len(t.entries) - 1; index >= 0; index-- {
		switch t.entries[index].Kind {
		case EntryToolPending, EntryTool, EntryToolError:
			if t.entries[index].ToolCallID == callID {
				return index
			}
		}
	}
	return -1
}

func (t *Transcript) finishPendingTools(outcome string) {
	for index := range t.entries {
		if t.entries[index].Kind != EntryToolPending {
			continue
		}
		t.entries[index].Kind = EntryToolError
		t.entries[index].Tool.Outcome = outcome
		t.changed(index)
	}
}

func description(entries []Entry) string {
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
