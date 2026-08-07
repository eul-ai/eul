package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"yaah/agent"
)

const (
	subagentToolName       = "subagent"
	maxSubagents           = 4
	subagentUpdateInterval = time.Second
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
	run func(context.Context, string, func(agent.Usage)) (agent.RunResult, error)
}

type subagentArguments struct {
	Tasks []string `json:"tasks"`
}

type subagentResult struct {
	text  string
	usage agent.Usage
	err   error
}

type subagentCompletion struct {
	index   int
	elapsed time.Duration
	result  subagentResult
}

type subagentProgress struct {
	index int
	usage agent.Usage
}

type subagentStatus struct {
	state   string
	started time.Time
	elapsed time.Duration
	tokens  int64
}

func NewSubagent(run func(context.Context, string, func(agent.Usage)) (agent.RunResult, error)) *Subagent {
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
	return subagentPresentation(tasks, nil, time.Time{})
}

func (s *Subagent) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[subagentArguments](arguments)
	if err != nil {
		return errorResult(subagentToolName, err), nil
	}
	if err := validateSubagentTasks(args.Tasks); err != nil {
		return errorResult(subagentToolName, err), nil
	}

	results, err := s.collectResults(ctx, args.Tasks, updates)
	if err != nil {
		return agent.ToolResult{}, err
	}
	return formatSubagentResults(results), nil
}

func validateSubagentTasks(tasks []string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("at least one task is required")
	}
	if len(tasks) > maxSubagents {
		return fmt.Errorf("tasks must not exceed %d", maxSubagents)
	}
	for _, task := range tasks {
		if strings.TrimSpace(task) == "" {
			return fmt.Errorf("tasks must be nonempty")
		}
	}
	return nil
}

func (s *Subagent) collectResults(ctx context.Context, tasks []string, updates agent.ToolUpdateSink) ([]subagentResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	started := time.Now()
	statuses := make([]subagentStatus, len(tasks))
	for index := range statuses {
		statuses[index] = subagentStatus{state: "running", started: started}
	}
	if err := publishSubagentUpdate(updates, tasks, statuses, started); err != nil {
		return nil, err
	}

	completions := make(chan subagentCompletion, len(tasks))
	progress := make(chan subagentProgress, len(tasks))
	for index, task := range tasks {
		go func(index int, task string) {
			result, runErr := s.run(runCtx, task, func(usage agent.Usage) {
				select {
				case progress <- subagentProgress{index: index, usage: usage}:
				case <-runCtx.Done():
				}
			})
			completions <- subagentCompletion{
				index:   index,
				elapsed: time.Since(started),
				result:  subagentResult{text: result.Text, usage: result.Usage, err: runErr},
			}
		}(index, task)
	}

	ticker := time.NewTicker(subagentUpdateInterval)
	defer ticker.Stop()

	results := make([]subagentResult, len(tasks))
	remaining := len(tasks)
	var updateErr error
	for remaining > 0 {
		select {
		case completion := <-completions:
			remaining--
			results[completion.index] = completion.result
			status := &statuses[completion.index]
			status.elapsed = completion.elapsed
			if completion.result.usage.TotalTokens > 0 {
				status.tokens = completion.result.usage.TotalTokens
			}
			if completion.result.err != nil {
				status.state = "failed"
			} else {
				status.state = "complete"
			}
			if updateErr == nil {
				updateErr = publishSubagentUpdate(updates, tasks, statuses, time.Now())
				if updateErr != nil {
					cancel()
				}
			}
		case childProgress := <-progress:
			if statuses[childProgress.index].state != "running" {
				continue
			}
			statuses[childProgress.index].tokens = childProgress.usage.TotalTokens
			if updateErr == nil {
				updateErr = publishSubagentUpdate(updates, tasks, statuses, time.Now())
				if updateErr != nil {
					cancel()
				}
			}
		case now := <-ticker.C:
			if updateErr == nil {
				updateErr = publishSubagentUpdate(updates, tasks, statuses, now)
				if updateErr != nil {
					cancel()
				}
			}
		}
	}

	if updateErr != nil {
		return nil, updateErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func formatSubagentResults(results []subagentResult) agent.ToolResult {
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
	return agent.ToolResult{Output: formatted, IsError: failed}
}

func publishSubagentUpdate(updates agent.ToolUpdateSink, tasks []string, statuses []subagentStatus, now time.Time) error {
	if updates == nil {
		return nil
	}
	return updates(subagentPresentation(tasks, statuses, now))
}

func subagentPresentation(tasks []string, statuses []subagentStatus, now time.Time) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: subagentToolName, Markdown: true}
	if len(tasks) > 1 {
		presentation.Arguments = fmt.Sprintf("(%d)", len(tasks))
	}
	presentation.Lines = make([]string, len(tasks))
	for index, task := range tasks {
		status := "pending"
		if index < len(statuses) {
			status = formatSubagentStatus(statuses[index], now)
		}
		label := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
		label = boundPresentationLabel(label, 120)
		presentation.Lines[index] = fmt.Sprintf("%d. %s — %s", index+1, status, label)
	}
	return presentation
}

func formatSubagentStatus(status subagentStatus, now time.Time) string {
	elapsed := status.elapsed
	if status.state == "running" {
		elapsed = now.Sub(status.started)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	details := []string{elapsed.Truncate(time.Second).String()}
	if status.tokens > 0 {
		details = append(details, fmt.Sprintf("%d tokens", status.tokens))
	}
	return fmt.Sprintf("%s (%s)", status.state, strings.Join(details, ", "))
}

func boundPresentationLabel(label string, maximum int) string {
	if len(label) <= maximum {
		return label
	}
	bounded, _ := truncateLine(label, maximum-3)
	return bounded + "..."
}
