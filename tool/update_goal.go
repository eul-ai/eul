package tool

import (
	"context"
	"encoding/json"
	"errors"

	"yaah/agent"
)

const updateGoalToolName = "update_goal"

var updateGoalToolDefinition = agent.ToolDefinition{
	Name: updateGoalToolName,
	Description: "Mark the active goal complete only after verifying that every requirement is achieved. " +
		"Do not call this tool for partial progress, blockers, cancellation, or ordinary tasks without an active goal.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"status": {Type: "string", Description: `Must be "complete".`},
	}, "status"),
}

type UpdateGoal struct {
	complete func() error
}

type updateGoalArguments struct {
	Status string `json:"status"`
}

func NewUpdateGoal(complete func() error) *UpdateGoal {
	return &UpdateGoal{complete: complete}
}

func (*UpdateGoal) Definition() agent.ToolDefinition {
	return updateGoalToolDefinition
}

func (*UpdateGoal) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	arguments := snapshotString(snapshot, "status")
	return agent.ToolPresentation{Title: updateGoalToolName, Arguments: arguments}
}

func (tool *UpdateGoal) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[updateGoalArguments](arguments)
	if err != nil {
		return errorResult(updateGoalToolName, err), nil
	}
	if args.Status != "complete" {
		return errorResult(updateGoalToolName, errors.New(`status must be "complete"`)), nil
	}
	if tool.complete == nil {
		return errorResult(updateGoalToolName, errors.New("goal completion is unavailable")), nil
	}
	if err := tool.complete(); err != nil {
		return errorResult(updateGoalToolName, err), nil
	}
	return successResult("Goal marked complete."), nil
}
