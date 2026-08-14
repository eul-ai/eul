package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

const (
	launchToolName       = "subagent"
	waitToolName         = "subagent_wait"
	cancelToolName       = "subagent_cancel"
	defaultWaitTimeout   = 30 * time.Second
	maximumWaitTimeout   = time.Hour
	defaultWaitTimeoutMS = int(defaultWaitTimeout / time.Millisecond)
	maximumWaitTimeoutMS = int(maximumWaitTimeout / time.Millisecond)
)

var taskSchema = StrictObject(map[string]agent.JSONSchema{
	"description": {
		Type:        "string",
		Description: "A short description of the task for progress display.",
	},
	"prompt": {
		Type:        "string",
		Description: "The complete task prompt. Include all context the subagent needs.",
	},
	"model_profile": {
		Type:        "string",
		Description: "fast, balanced, or main (default; the main agent's model).",
	},
	"thinking_level": {
		Type:        "string",
		Description: subagentThinkingLevelDescription(),
	},
}, "description", "prompt")

func subagentThinkingLevelDescription() string {
	levels := agent.ThinkingLevels()
	values := make([]string, len(levels))
	for index, level := range levels {
		values[index] = string(level)
	}
	return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1] + ". Defaults to the main agent's current level."
}

var launchDefinition = agent.ToolDefinition{
	Name:        launchToolName,
	Description: "Launch one to four independent read-only research tasks, with at most four active. Terminal results are delivered automatically. Returned IDs are only for status and cancellation. Omit model profile and thinking level to inherit the main agent's settings. Override them only when explicitly requested or when there is a clear task-specific reason.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"tasks": {
			Type:        "array",
			Description: "One to four independent tasks, each with its own model profile and thinking level.",
			Items:       &taskSchema,
		},
	}, "tasks"),
}

var waitDefinition = agent.ToolDefinition{
	Name:        waitToolName,
	Description: "Wait sparingly when no independent work remains and the next step requires a subagent result. Completion notifications are delivered automatically. Steering interrupts the wait.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"timeout_ms": nullable("integer", fmt.Sprintf("Optional timeout in milliseconds; null defaults to %d, maximum %d.", defaultWaitTimeoutMS, maximumWaitTimeoutMS)),
	}),
}

var cancelDefinition = agent.ToolDefinition{
	Name:        cancelToolName,
	Description: "Cancel selected active subagents. Their terminal cancellation notifications are delivered automatically.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"ids": {
			Type:        "array",
			Description: "One to four active subagent IDs returned by the subagent tool.",
			Items:       &agent.JSONSchema{Type: "string"},
		},
	}, "ids"),
}

type subagentTask struct {
	Description   string  `json:"description"`
	Prompt        string  `json:"prompt"`
	ModelProfile  *string `json:"model_profile"`
	ThinkingLevel *string `json:"thinking_level"`
}

type launchArguments struct {
	Tasks []subagentTask `json:"tasks"`
}

type waitArguments struct {
	TimeoutMS *int `json:"timeout_ms"`
}

type cancelArguments struct {
	IDs []string `json:"ids"`
}

type launchTool struct {
	manager *subagent.Manager
}

type waitTool struct {
	manager *subagent.Manager
}

type cancelTool struct {
	manager *subagent.Manager
}

func NewSubagent(manager *subagent.Manager) Tool {
	return &launchTool{manager: manager}
}

func NewSubagentWait(manager *subagent.Manager) Tool {
	return &waitTool{manager: manager}
}

func NewSubagentCancel(manager *subagent.Manager) Tool {
	return &cancelTool{manager: manager}
}

func (*launchTool) Definition() agent.ToolDefinition {
	return launchDefinition
}

func (launch *launchTool) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["tasks"].([]any)
	return agent.ToolPresentation{
		Title:     launchToolName,
		Arguments: fmt.Sprintf("(%d)", len(values)),
		Markdown:  true,
		Lines:     []string{fmt.Sprintf("Starting %d subagent(s).", len(values))},
	}
}

func (launch *launchTool) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[launchArguments](arguments)
	if err != nil {
		return errorResult(launchToolName, err), nil
	}

	jobs, err := launch.manager.Start(toSubagentTasks(args.Tasks))
	if err != nil {
		return errorResult(launchToolName, err), nil
	}

	var output strings.Builder
	output.WriteString("Started subagents:")
	for _, job := range jobs {
		fmt.Fprintf(&output, "\n- %s (%s, %s thinking): %s", job.ID, job.ModelProfile, job.ThinkingLevel, strings.TrimSpace(job.Description))
	}
	if updates != nil {
		updates.SetFinal(agent.ToolPresentation{
			Title:     launchToolName,
			Arguments: fmt.Sprintf("(%d)", len(jobs)),
			Markdown:  true,
			Lines:     []string{fmt.Sprintf("Started %d subagent(s).", len(jobs))},
		})
	}
	return successResult(output.String()), nil
}

func (*waitTool) Definition() agent.ToolDefinition {
	return waitDefinition
}

func (*waitTool) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	var timeout time.Duration
	if number, ok := snapshot.Arguments["timeout_ms"].(json.Number); ok {
		if milliseconds, err := number.Int64(); err == nil {
			timeout = time.Duration(milliseconds) * time.Millisecond
		}
	}

	return waitPresentation(timeout, "Waiting for a subagent completion.")
}

func waitPresentation(timeout time.Duration, message string) agent.ToolPresentation {
	arguments := ""
	if timeout > 0 {
		arguments = fmt.Sprintf("(%s timeout)", timeout)
	}

	return agent.ToolPresentation{Title: waitToolName, Arguments: arguments, Markdown: true, Lines: []string{message}}
}

func (wait *waitTool) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	args, err := DecodeArguments[waitArguments](arguments)
	if err != nil {
		return errorResult(waitToolName, err), nil
	}
	timeoutMS, err := optionalPositive(args.TimeoutMS, defaultWaitTimeoutMS, maximumWaitTimeoutMS, "timeout_ms")
	if err != nil {
		return errorResult(waitToolName, err), nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	outcome, err := wait.manager.Wait(waitCtx)
	if err != nil {
		if ctx.Err() != nil {
			return agent.ToolResult{}, ctx.Err()
		}
		return errorResult(waitToolName, err), nil
	}

	message := waitResultMessage(outcome)
	if updates != nil {
		var timeout time.Duration
		if args.TimeoutMS != nil {
			timeout = time.Duration(timeoutMS) * time.Millisecond
		}
		updates.SetFinal(waitPresentation(timeout, message))
	}
	return successResult(message), nil
}

func waitResultMessage(outcome subagent.WaitOutcome) string {
	switch outcome {
	case subagent.WaitCompletion:
		return "A subagent completion is available."
	case subagent.WaitSteering:
		return "Wait interrupted by steering. The subagents remain active. Address the steering input, then continue the original task; if it still requires their results and no independent work remains, call subagent_wait again instead of finishing."
	case subagent.WaitTimeout:
		return "No subagent completion is available yet."
	default:
		return ""
	}
}

func (*cancelTool) Definition() agent.ToolDefinition {
	return cancelDefinition
}

func (*cancelTool) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["ids"].([]any)
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok {
			lines = append(lines, id)
		}
	}
	return agent.ToolPresentation{Title: cancelToolName, Markdown: true, Lines: lines}
}

func (cancel *cancelTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[cancelArguments](arguments)
	if err != nil {
		return errorResult(cancelToolName, err), nil
	}
	canceled, err := cancel.manager.Cancel(args.IDs)
	if err != nil {
		return errorResult(cancelToolName, err), nil
	}
	return successResult("Canceling subagents:\n- " + strings.Join(canceled, "\n- ")), nil
}

func toSubagentTasks(tasks []subagentTask) []subagent.Task {
	converted := make([]subagent.Task, len(tasks))
	for index, task := range tasks {
		converted[index] = subagent.Task{Description: task.Description, Prompt: task.Prompt}
		if task.ModelProfile != nil {
			converted[index].ModelProfile = subagent.Profile(*task.ModelProfile)
		}
		if task.ThinkingLevel != nil {
			converted[index].ThinkingLevel = agent.ThinkingLevel(*task.ThinkingLevel)
		}
	}
	return converted
}
