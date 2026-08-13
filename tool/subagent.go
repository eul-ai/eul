package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

const (
	maxActiveSubagents   = 4
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
}, "description", "prompt")

var launchDefinition = agent.ToolDefinition{
	Name:        launchToolName,
	Description: "Launch one to four independent read-only research tasks, with at most four active. Terminal results are delivered automatically. Returned IDs are only for status and cancellation.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"tasks": {
			Type:        "array",
			Description: "One to four independent tasks.",
			Items:       &taskSchema,
		},
		"model_profile": {
			Type:        "string",
			Description: "fast, balanced (default), or powerful.",
		},
		"thinking_level": {
			Type:        "string",
			Description: "off, minimal, low (default), medium, or high.",
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
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

type launchArguments struct {
	Tasks         []subagentTask `json:"tasks"`
	ModelProfile  *string        `json:"model_profile"`
	ThinkingLevel *string        `json:"thinking_level"`
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
	profile := subagent.ProfileBalanced
	if value, ok := snapshot.Arguments["model_profile"].(string); ok {
		profile = subagent.Profile(value)
	}
	thinkingLevel := agent.ThinkingLow
	if value, ok := snapshot.Arguments["thinking_level"].(string); ok {
		thinkingLevel = agent.ThinkingLevel(value)
	}
	return agent.ToolPresentation{
		Title:     launchToolName,
		Arguments: fmt.Sprintf("(%s, %s)", profile, thinkingLevel),
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
	if err := validateTasks(args.Tasks); err != nil {
		return errorResult(launchToolName, err), nil
	}
	profile, err := resolveSubagentProfile(args.ModelProfile)
	if err != nil {
		return errorResult(launchToolName, err), nil
	}
	thinkingLevel, err := resolveSubagentThinkingLevel(launch.manager, profile, args.ThinkingLevel)
	if err != nil {
		return errorResult(launchToolName, err), nil
	}

	jobs, err := launch.manager.Start(toSubagentTasks(args.Tasks), profile, thinkingLevel)
	if err != nil {
		return errorResult(launchToolName, err), nil
	}

	var output strings.Builder
	fmt.Fprintf(&output, "Started subagents (model: %s, thinking: %s):", profile, thinkingLevel)
	for _, job := range jobs {
		fmt.Fprintf(&output, "\n- %s: %s", job.ID, strings.TrimSpace(job.Description))
	}
	if updates != nil {
		updates.SetFinal(agent.ToolPresentation{
			Title:     launchToolName,
			Arguments: fmt.Sprintf("(%s, %s)", profile, thinkingLevel),
			Markdown:  true,
			Lines:     []string{fmt.Sprintf("Started %d subagent(s).", len(jobs))},
		})
	}
	return successResult(output.String()), nil
}

func (*waitTool) Definition() agent.ToolDefinition {
	return waitDefinition
}

func (*waitTool) Presentation(PresentationSnapshot) agent.ToolPresentation {
	return agent.ToolPresentation{Title: waitToolName, Markdown: true, Lines: []string{"Waiting for a subagent completion."}}
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
		updates.SetFinal(agent.ToolPresentation{Title: waitToolName, Markdown: true, Lines: []string{message}})
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
	if err := validateIDs(args.IDs); err != nil {
		return errorResult(cancelToolName, err), nil
	}
	canceled, err := cancel.manager.Cancel(args.IDs)
	if err != nil {
		return errorResult(cancelToolName, err), nil
	}
	return successResult("Canceling subagents:\n- " + strings.Join(canceled, "\n- ")), nil
}

func validateTasks(tasks []subagentTask) error {
	if len(tasks) == 0 {
		return errors.New("at least one task is required")
	}
	if len(tasks) > maxActiveSubagents {
		return fmt.Errorf("tasks must not exceed %d", maxActiveSubagents)
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.Description) == "" {
			return errors.New("task descriptions must be nonempty")
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return errors.New("task prompts must be nonempty")
		}
	}
	return nil
}

func toSubagentTasks(tasks []subagentTask) []subagent.Task {
	converted := make([]subagent.Task, len(tasks))
	for index, task := range tasks {
		converted[index] = subagent.Task{Description: task.Description, Prompt: task.Prompt}
	}
	return converted
}

func resolveSubagentProfile(value *string) (subagent.Profile, error) {
	profile := subagent.ProfileBalanced
	if value != nil {
		profile = subagent.Profile(*value)
	}
	switch profile {
	case subagent.ProfileFast, subagent.ProfileBalanced, subagent.ProfilePowerful:
		return profile, nil
	default:
		return "", errors.New("model profile must be one of fast, balanced, or powerful")
	}
}

func resolveSubagentThinkingLevel(manager *subagent.Manager, profile subagent.Profile, value *string) (agent.ThinkingLevel, error) {
	supported := manager.SupportedThinkingLevels(profile)
	level := agent.ThinkingLow
	if value == nil {
		level = agent.ClampThinkingLevel(level, supported)
	} else {
		level = agent.ThinkingLevel(*value)
	}

	switch level {
	case agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh:
	case agent.ThinkingXHigh, agent.ThinkingMax:
		return "", fmt.Errorf("thinking level %q is not available to subagents; use off, minimal, low, medium, or high", level)
	default:
		return "", errors.New("thinking level must be one of off, minimal, low, medium, or high")
	}
	if !slices.Contains(supported, level) {
		return "", fmt.Errorf("thinking level %q is not supported by the %s model", level, profile)
	}
	return level, nil
}

func validateIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("at least one subagent ID is required")
	}
	if len(ids) > maxActiveSubagents {
		return fmt.Errorf("subagent IDs must not exceed %d", maxActiveSubagents)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errors.New("subagent IDs must be nonempty")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate subagent ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
