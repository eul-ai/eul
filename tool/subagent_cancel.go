package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const subagentCancelToolName = "subagent_cancel"

var subagentCancelToolDefinition = agent.ToolDefinition{
	Name:        subagentCancelToolName,
	Description: "Cancel selected background subagents. Use this when their work is no longer needed; canceled results remain available to subagent_wait until collected.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"ids": {
			Type:        "array",
			Description: "One to four subagent IDs returned by the subagent tool.",
			Items:       &agent.JSONSchema{Type: "string"},
		},
	}, "ids"),
}

type SubagentCancel struct {
	subagents *Subagent
}

type subagentCancelArguments struct {
	IDs []string `json:"ids"`
}

func NewSubagentCancel(subagents *Subagent) *SubagentCancel {
	return &SubagentCancel{subagents: subagents}
}

func (*SubagentCancel) Definition() agent.ToolDefinition {
	return subagentCancelToolDefinition
}

func (*SubagentCancel) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	values, _ := snapshot.Arguments["ids"].([]any)
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok {
			ids = append(ids, id)
		}
	}
	presentation := agent.ToolPresentation{Title: subagentCancelToolName, Markdown: true}
	if len(ids) > 1 {
		presentation.Arguments = fmt.Sprintf("(%d)", len(ids))
	}
	presentation.Lines = ids
	return presentation
}

func (cancel *SubagentCancel) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[subagentCancelArguments](arguments)
	if err != nil {
		return errorResult(subagentCancelToolName, err), nil
	}
	if err := validateSubagentIDs(args.IDs); err != nil {
		return errorResult(subagentCancelToolName, err), nil
	}
	jobs, err := cancel.subagents.jobsForIDs(args.IDs)
	if err != nil {
		return errorResult(subagentCancelToolName, err), nil
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	canceled := cancel.subagents.cancelJobs(jobs, errSubagentCanceled)
	if len(canceled) == 0 {
		return successResult("Selected subagents were already settled."), nil
	}
	return successResult("Canceled subagents:\n- " + strings.Join(canceled, "\n- ")), nil
}
