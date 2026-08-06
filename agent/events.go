package agent

// EventKind identifies an observable agent-engine event.
type EventKind string

const (
	EventAssistantText      EventKind = "assistant_text"
	EventAssistantReasoning EventKind = "assistant_reasoning"
	EventToolStart          EventKind = "tool_start"
	EventToolEnd            EventKind = "tool_end"
)

// Event reports assistant output and tool execution lifecycle changes.
type Event struct {
	Kind   EventKind
	Text   string
	Call   ToolCall
	Result ToolResult
}

// EventSink receives observable engine events.
type EventSink func(Event) error
