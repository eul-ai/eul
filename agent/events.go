package agent

type EventKind string

const (
	EventAssistantText      EventKind = "assistant_text"
	EventAssistantReasoning EventKind = "assistant_reasoning"
	EventToolStart          EventKind = "tool_start"
	EventToolEnd            EventKind = "tool_end"
)

type Event struct {
	Kind   EventKind
	Text   string
	Call   ToolCall
	Result ToolResult
}

type EventSink func(Event) error
