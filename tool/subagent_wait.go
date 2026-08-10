package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

func (wait *SubagentWait) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["ids"].([]any)
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok {
			ids = append(ids, id)
		}
	}
	if wait.subagents == nil {
		return subagentWaitPresentation(ids, nil, time.Now())
	}
	return subagentWaitPresentation(ids, wait.subagents.snapshotsForPresentation(ids), time.Now())
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

	snapshots, err := wait.collect(ctx, jobs, updates)
	if err != nil {
		if ctx.Err() != nil {
			wait.subagents.cancelJobs(jobs, errSubagentCanceled)
		}
		return agent.ToolResult{}, err
	}
	result := formatSubagentResults(snapshots)
	wait.subagents.consume(jobs)
	return result, nil
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

func (wait *SubagentWait) collect(ctx context.Context, jobs []*subagentJob, updates agent.ToolUpdateSink) ([]subagentJobSnapshot, error) {
	ticker := time.NewTicker(subagentUpdateInterval)
	defer ticker.Stop()

	for {
		now := time.Now()
		snapshots, complete := wait.subagents.snapshotJobs(jobs)
		if err := publishSubagentWaitUpdate(updates, snapshots, now); err != nil {
			return nil, err
		}
		if complete {
			return snapshots, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait.subagents.changes:
		case <-ticker.C:
		}
	}
}

func formatSubagentResults(snapshots []subagentJobSnapshot) agent.ToolResult {
	var output strings.Builder
	output.WriteString(subagentResultGuidance)
	failed := false
	for _, snapshot := range snapshots {
		output.WriteString("\n\n")
		fmt.Fprintf(&output, "Subagent %s (model: %s, thinking: %s):\n", snapshot.id, snapshot.modelProfile, snapshot.thinkingLevel)
		if snapshot.result.text != "" {
			output.WriteString(snapshot.result.text)
		}
		if snapshot.result.err != nil {
			failed = true
			if snapshot.result.text != "" {
				output.WriteString("\n\n")
			}
			fmt.Fprintf(&output, "error: %v", snapshot.result.err)
		}
	}

	formatted := output.String()
	if truncateHead(formatted, defaultMaxLines, defaultMaxBytes).truncated {
		formatted = boundHead(formatted, "subagent output truncated")
	}
	return agent.ToolResult{Output: formatted, IsError: failed}
}

func publishSubagentWaitUpdate(updates agent.ToolUpdateSink, snapshots []subagentJobSnapshot, now time.Time) error {
	if updates == nil {
		return nil
	}
	return updates.Update(subagentWaitPresentation(nil, snapshots, now))
}

func subagentWaitPresentation(ids []string, snapshots []subagentJobSnapshot, now time.Time) agent.ToolPresentation {
	count := len(snapshots)
	if count == 0 {
		count = len(ids)
	}
	presentation := agent.ToolPresentation{Title: subagentWaitToolName, Markdown: true}
	if count > 1 {
		presentation.Arguments = fmt.Sprintf("(%d)", count)
	}
	presentation.Lines = make([]string, count)
	for index := range count {
		id := ""
		if index < len(ids) {
			id = ids[index]
		}
		status := subagentStatus{state: "pending"}
		task := ""
		if index < len(snapshots) {
			id = snapshots[index].id
			status = snapshots[index].status
			task = snapshots[index].task
		}

		line := fmt.Sprintf("%d. %s — %s", index+1, formatSubagentStatus(status, now), id)
		if index < len(snapshots) && snapshots[index].modelProfile != "" && snapshots[index].thinkingLevel != "" {
			line += " (" + string(snapshots[index].modelProfile) + ", " + string(snapshots[index].thinkingLevel) + ")"
		}
		if label := boundPresentationLabel(strings.TrimSpace(strings.SplitN(task, "\n", 2)[0]), 120); label != "" {
			line += " — " + label
		}
		presentation.Lines[index] = line
	}
	return presentation
}
