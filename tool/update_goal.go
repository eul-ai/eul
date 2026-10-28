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
	Description: "Complete the active goal after verifying all requirements; succeeds without changes if no incomplete goal is active.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"status": {Type: "string", Description: `Must be "complete".`},
	}, "status"),
}

type updateGoalTool struct {
	complete func() error
}

type updateGoalArguments struct {
	Status string `json:"status"`
}

func NewUpdateGoal(complete func() error) Tool {
	return &updateGoalTool{complete: complete}
}

func (*updateGoalTool) Definition() agent.ToolDefinition {
	return updateGoalToolDefinition
}

func (*updateGoalTool) Presentation(snapshot presentationSnapshot) agent.ToolPresentation {
	arguments := snapshotString(snapshot, "status")
	return agent.ToolPresentation{Title: updateGoalToolName, Arguments: arguments}
}

func (tool *updateGoalTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
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
		switch {
		case errors.Is(err, agent.ErrNoGoal):
			return successResult("No active goal."), nil
		case errors.Is(err, agent.ErrGoalAlreadyComplete):
			return successResult("Goal already complete."), nil
		default:
			return errorResult(updateGoalToolName, err), nil
		}
	}
	return successResult("Goal marked complete."), nil
}
