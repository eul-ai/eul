package agent

type EventKind string

const (
	EventAssistantText      EventKind = "assistant_text"
	EventAssistantReasoning EventKind = "assistant_reasoning"
	EventCompactionStart    EventKind = "compaction_start"
	EventCompactionEnd      EventKind = "compaction_end"
	EventContextUsage       EventKind = "context_usage"
	EventToolStart          EventKind = "tool_start"
	EventToolEnd            EventKind = "tool_end"
)

type Event struct {
	Kind   EventKind
	Text   string
	Call   ToolCall
	Result ToolResult
	Usage  Usage
}

type EventSink func(Event) error
