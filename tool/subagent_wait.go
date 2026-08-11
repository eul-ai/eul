package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const (
	subagentWaitToolName   = "subagent_wait"
	subagentResultGuidance = "Use these results in the eventual user response and continue in the main context; do not launch follow-up subagents for the same objective unless the user asks."
)

var subagentWaitToolDefinition = agent.ToolDefinition{
	Name:        subagentWaitToolName,
	Description: "Wait for selected background subagents and return their results, which are collected once returned. When possible, continue useful independent work before waiting. After waiting, synthesize the findings and continue directly instead of launching follow-up subagents.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"ids": {
			Type:        "array",
			Description: "One to four subagent IDs returned by the subagent tool.",
			Items:       &agent.JSONSchema{Type: "string"},
		},
	}, "ids"),
}

type SubagentWait struct {
	subagents *Subagent
}

type subagentWaitArguments struct {
	IDs []string `json:"ids"`
}

func NewSubagentWait(subagents *Subagent) *SubagentWait {
	return &SubagentWait{subagents: subagents}
}

func (*SubagentWait) Definition() agent.ToolDefinition {
	return subagentWaitToolDefinition
}

func (*SubagentWait) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["ids"].([]any)
	return subagentWaitPresentation(len(values), false)
}

func (wait *SubagentWait) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[subagentWaitArguments](arguments)
	if err != nil {
		return errorResult(subagentWaitToolName, err), nil
	}
	if err := validateSubagentIDs(args.IDs); err != nil {
		return errorResult(subagentWaitToolName, err), nil
	}

	jobs, err := wait.subagents.jobsForIDs(args.IDs)
	if err != nil {
		return errorResult(subagentWaitToolName, err), nil
	}
	if err := ctx.Err(); err != nil {
		wait.subagents.cancelJobs(jobs, errSubagentCanceled)
		return agent.ToolResult{}, err
	}

	snapshots, err := wait.collect(ctx, jobs)
	if err != nil {
		if ctx.Err() != nil {
			wait.subagents.cancelJobs(jobs, errSubagentCanceled)
		}
		return agent.ToolResult{}, err
	}
	result := formatSubagentResults(snapshots)
	wait.subagents.consume(jobs)
	if updates != nil {
		updates.SetFinal(subagentWaitPresentation(len(args.IDs), true))
	}
	return result, nil
}

func subagentWaitPresentation(count int, complete bool) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: subagentWaitToolName, Markdown: true}
	if count == 0 {
		return presentation
	}

	state := "Waiting for"
	if complete {
		state = "Waited for"
	}
	presentation.Lines = []string{fmt.Sprintf("%s %d subagent(s).", state, count)}
	return presentation
}

func validateSubagentIDs(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one subagent ID is required")
	}
	if len(ids) > maxSubagents {
		return fmt.Errorf("subagent IDs must not exceed %d", maxSubagents)
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("subagent IDs must be nonempty")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate subagent ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (wait *SubagentWait) collect(ctx context.Context, jobs []*subagentJob) ([]subagentJobSnapshot, error) {
	for {
		snapshots, complete, changes := wait.subagents.snapshotJobs(jobs)
		if complete {
			return snapshots, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changes:
		}
	}
}

func formatSubagentResults(snapshots []subagentJobSnapshot) agent.ToolResult {
	var output strings.Builder
	output.WriteString(subagentResultGuidance)
	failed := false
	if len(snapshots) == 0 {
		return agent.ToolResult{Output: output.String()}
	}

	sectionBytes := (defaultMaxBytes - len(subagentResultGuidance)) / len(snapshots)
	sectionLines := (defaultMaxLines - 1) / len(snapshots)
	for _, snapshot := range snapshots {
		heading := fmt.Sprintf("\n\nSubagent %s (model: %s, thinking: %s):\n", snapshot.id, snapshot.modelProfile, snapshot.thinkingLevel)
		output.WriteString(heading)

		body := snapshot.result.text
		if snapshot.result.err != nil {
			failed = true
			if body != "" {
				body += "\n\n"
			}
			body += fmt.Sprintf("error: %v", snapshot.result.err)
		}
		output.WriteString(boundSubagentResult(body, sectionLines-3, sectionBytes-len(heading)))
	}

	formatted := output.String()
	if truncateHead(formatted, defaultMaxLines, defaultMaxBytes).truncated {
		formatted = boundHead(formatted, "subagent output truncated")
	}
	return agent.ToolResult{Output: formatted, IsError: failed}
}

func boundSubagentResult(body string, maxLines, maxBytes int) string {
	bounded := truncateHead(body, maxLines, maxBytes)
	if !bounded.truncated {
		return bounded.text
	}

	const marker = "[subagent result truncated]"
	content := truncateHead(body, maxLines-1, maxBytes-len(marker)-1).text
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return truncateHead(marker, maxLines, maxBytes).text
	}
	return content + "\n" + marker
}
