package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"yaah/agent"
)

const (
	subagentToolName = "subagent"
	maxSubagents     = 4
)

var subagentToolDefinition = agent.ToolDefinition{
	Name:        subagentToolName,
	Description: "Run independent read-only tasks concurrently and wait for all results. Use only when the user explicitly asks for subagents.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"tasks": {
			Type:        "array",
			Description: "One to four independent tasks. Include all context each subagent needs.",
			Items:       &agent.JSONSchema{Type: "string"},
		},
	}, "tasks"),
}

type Subagent struct {
	run func(context.Context, string) (agent.RunResult, error)
}

type subagentArguments struct {
	Tasks []string `json:"tasks"`
}

type subagentResult struct {
	text string
	err  error
}

type subagentCompletion struct {
	index  int
	result subagentResult
}

func NewSubagent(run func(context.Context, string) (agent.RunResult, error)) *Subagent {
	return &Subagent{run: run}
}

func (*Subagent) Definition() agent.ToolDefinition {
	return subagentToolDefinition
}

func (*Subagent) Presentation(snapshot agent.ToolCallSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["tasks"].([]any)
	tasks := make([]string, 0, len(values))
	for _, value := range values {
		if task, ok := value.(string); ok {
			tasks = append(tasks, task)
		}
	}
	return subagentPresentation(tasks, "pending", nil)
}

func (s *Subagent) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[subagentArguments](arguments)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}
	if len(args.Tasks) == 0 {
		return errorResult(subagentToolName, fmt.Errorf("at least one task is required")), nil
	}
	if len(args.Tasks) > maxSubagents {
		return errorResult(subagentToolName, fmt.Errorf("tasks must not exceed %d", maxSubagents)), nil
	}
	for _, task := range args.Tasks {
		if strings.TrimSpace(task) == "" {
			return errorResult(subagentToolName, fmt.Errorf("tasks must be nonempty")), nil
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	statuses := make([]string, len(args.Tasks))
	for index := range statuses {
		statuses[index] = "running"
	}
	if err := publishSubagentUpdate(updates, args.Tasks, statuses); err != nil {
		return agent.ToolResult{}, err
	}

	completions := make(chan subagentCompletion, len(args.Tasks))
	for index, task := range args.Tasks {
		go func() {
			result, runErr := s.run(runCtx, task)
			completions <- subagentCompletion{
				index:  index,
				result: subagentResult{text: result.Text, err: runErr},
			}
		}()
	}

	results := make([]subagentResult, len(args.Tasks))
	var updateErr error
	for range args.Tasks {
		completion := <-completions
		results[completion.index] = completion.result
		if completion.result.err != nil {
			statuses[completion.index] = "failed"
		} else {
			statuses[completion.index] = "complete"
		}
		if updateErr == nil {
			updateErr = publishSubagentUpdate(updates, args.Tasks, statuses)
			if updateErr != nil {
				cancel()
			}
		}
	}

	if updateErr != nil {
		return agent.ToolResult{}, updateErr
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	var output strings.Builder
	failed := false
	for index, result := range results {
		if index > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "Subagent %d:\n", index+1)
		if result.err != nil {
			failed = true
			fmt.Fprintf(&output, "error: %v", result.err)
			continue
		}
		output.WriteString(result.text)
	}

	formatted := output.String()
	if truncateHead(formatted, defaultMaxLines, defaultMaxBytes).truncated {
		formatted = boundHead(formatted, "subagent output truncated")
	}
	return agent.ToolResult{Output: formatted, IsError: failed}, nil
}

func publishSubagentUpdate(updates agent.ToolUpdateSink, tasks, statuses []string) error {
	if updates == nil {
		return nil
	}
	return updates(subagentPresentation(tasks, "", statuses))
}

func subagentPresentation(tasks []string, defaultStatus string, statuses []string) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: subagentToolName, Markdown: true}
	if len(tasks) > 1 {
		presentation.Arguments = fmt.Sprintf("(%d)", len(tasks))
	}
	presentation.Lines = make([]string, len(tasks))
	for index, task := range tasks {
		status := defaultStatus
		if index < len(statuses) {
			status = statuses[index]
		}
		label := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
		label = boundPresentationLabel(label, 120)
		presentation.Lines[index] = fmt.Sprintf("%d. %s — %s", index+1, status, label)
	}
	return presentation
}

func boundPresentationLabel(label string, maximum int) string {
	if len(label) <= maximum {
		return label
	}
	bounded, _ := truncateLine(label, maximum-3)
	return bounded + "..."
}
