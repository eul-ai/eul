package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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

func NewSubagent(run func(context.Context, string) (agent.RunResult, error)) *Subagent {
	return &Subagent{run: run}
}

func (*Subagent) Definition() agent.ToolDefinition {
	return subagentToolDefinition
}

func (s *Subagent) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
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

	results := make([]subagentResult, len(args.Tasks))
	var wait sync.WaitGroup
	wait.Add(len(args.Tasks))
	for index, task := range args.Tasks {
		go func() {
			defer wait.Done()

			result, err := s.run(ctx, task)
			results[index] = subagentResult{text: result.Text, err: err}
		}()
	}
	wait.Wait()

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
