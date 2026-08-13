package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/eul-ai/eul/agent"
)

const updateGoalToolName = "update_goal"

var updateGoalToolDefinition = agent.ToolDefinition{
	Name:        updateGoalToolName,
	Description: "Mark an active goal complete only when all requirements are verified.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
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

	args, err := DecodeArguments[updateGoalArguments](arguments)
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
