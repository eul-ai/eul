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
	launchToolName       = "launch_subagents"
	waitToolName         = "wait_for_subagent"
	cancelToolName       = "cancel_subagents"
	defaultWaitTimeout   = 30 * time.Second
	maximumWaitTimeout   = time.Hour
	defaultWaitTimeoutMS = int(defaultWaitTimeout / time.Millisecond)
	maximumWaitTimeoutMS = int(maximumWaitTimeout / time.Millisecond)
)

func launchDefinitionFor(manager *subagent.Manager) agent.ToolDefinition {
	taskSchema := StrictObject(map[string]agent.JSONSchema{
		"description": {
			Type:        "string",
			Description: "Short progress label.",
		},
		"prompt": {
			Type:        "string",
			Description: "Complete task with all needed context.",
		},
		"model_profile":  nullable("string", "fast, balanced, or main; null inherits the parent."),
		"thinking_level": nullable("string", subagentThinkingLevelDescription(manager)),
	}, "description", "prompt", "model_profile", "thinking_level")

	return agent.ToolDefinition{
		Name:        launchToolName,
		Description: "Launch 1-4 independent read-only research tasks, with at most 4 active. Results arrive automatically.",
		Parameters: StrictObject(map[string]agent.JSONSchema{
			"tasks": {
				Type:        "array",
				Description: "1-4 independent tasks.",
				Items:       &taskSchema,
			},
		}, "tasks"),
	}
}

func subagentThinkingLevelDescription(manager *subagent.Manager) string {
	if manager == nil {
		return formatThinkingLevelChoices(agent.ThinkingLevels()) + "; null inherits the parent."
	}

	profiles := []subagent.Profile{subagent.ProfileFast, subagent.ProfileBalanced, subagent.ProfileMain}
	choices := make([]string, len(profiles))
	for index, profile := range profiles {
		choices[index] = fmt.Sprintf("%s: %s", profile, formatThinkingLevelChoices(manager.SupportedThinkingLevels(profile)))
	}
	return "Supported by model profile: " + strings.Join(choices, "; ") + "; null inherits the parent."
}

func formatThinkingLevelChoices(levels []agent.ThinkingLevel) string {
	values := make([]string, len(levels))
	for index, level := range levels {
		values[index] = string(level)
	}

	switch len(values) {
	case 0:
		return "none"
	case 1:
		return values[0]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
	}
}

var waitDefinition = agent.ToolDefinition{
	Name:        waitToolName,
	Description: "Wait for subagent results only when no other work remains. Steering interrupts the wait.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"timeout_ms": nullable("integer", fmt.Sprintf("Timeout in milliseconds; null uses %d, maximum %d.", defaultWaitTimeoutMS, maximumWaitTimeoutMS)),
	}, "timeout_ms"),
}

var cancelDefinition = agent.ToolDefinition{
	Name:        cancelToolName,
	Description: "Cancel active subagents.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"ids": {
			Type:        "array",
			Description: "1-4 active subagent IDs.",
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
	manager    *subagent.Manager
	definition agent.ToolDefinition
}

type waitTool struct {
	manager *subagent.Manager
}

type cancelTool struct {
	manager *subagent.Manager
}

func NewLaunchSubagents(manager *subagent.Manager) Tool {
	return &launchTool{manager: manager, definition: launchDefinitionFor(manager)}
}

func NewWaitForSubagent(manager *subagent.Manager) Tool {
	return &waitTool{manager: manager}
}

func NewCancelSubagents(manager *subagent.Manager) Tool {
	return &cancelTool{manager: manager}
}

func (launch *launchTool) Definition() agent.ToolDefinition {
	return launch.definition
}

func (launch *launchTool) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["tasks"].([]any)
	return launchPresentation(len(values), fmt.Sprintf("Starting %d subagent(s).", len(values)))
}

func launchPresentation(count int, message string) agent.ToolPresentation {
	return agent.ToolPresentation{
		Title:     launchToolName,
		Arguments: fmt.Sprintf("(%d)", count),
		Markdown:  true,
		Lines:     []string{message},
	}
}

func launchFailurePresentation() agent.ToolPresentation {
	return agent.ToolPresentation{
		Title:    launchToolName,
		Markdown: true,
		Lines:    []string{"No subagents were started."},
	}
}

func (launch *launchTool) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[launchArguments](arguments)
	if err != nil {
		if updates != nil {
			updates.SetFinal(launchFailurePresentation())
		}
		return errorResult(launchToolName, err), nil
	}

	jobs, err := launch.manager.Start(toSubagentTasks(args.Tasks))
	if err != nil {
		if updates != nil {
			updates.SetFinal(launchFailurePresentation())
		}
		return errorResult(launchToolName, err), nil
	}

	var output strings.Builder
	output.WriteString("Started subagents:")
	for _, job := range jobs {
		fmt.Fprintf(&output, "\n- %s (%s, %s thinking): %s", job.ID, job.ModelProfile, job.ThinkingLevel, strings.TrimSpace(job.Description))
	}
	if updates != nil {
		updates.SetFinal(launchPresentation(len(jobs), fmt.Sprintf("Started %d subagent(s).", len(jobs))))
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
		return "Wait interrupted by steering. The subagents remain active. Address the steering input, then continue the original task; if it still requires their results and no independent work remains, call wait_for_subagent again instead of finishing."
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
