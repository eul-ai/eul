package agent

type EventKind string

const (
	EventAssistantText      EventKind = "assistant_text"
	EventAssistantReasoning EventKind = "assistant_reasoning"
	EventCompactionStart    EventKind = "compaction_start"
	EventCompactionEnd      EventKind = "compaction_end"
	EventContextUsage       EventKind = "context_usage"
	EventGenerationRetry    EventKind = "generation_retry"
	EventSteering           EventKind = "steering"
	EventGoalContinuation   EventKind = "goal_continuation"
	EventToolStart          EventKind = "tool_start"
	EventToolUpdate         EventKind = "tool_update"
	EventToolExecute        EventKind = "tool_execute"
	EventToolEnd            EventKind = "tool_end"
)

type Event struct {
	Kind         EventKind
	Text         string
	Call         ToolCall
	Presentation ToolPresentation
	Result       ToolResult
	Usage        Usage
	Attempt      int
}

type EventSink func(Event) error
