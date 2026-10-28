package tool

import (
	"context"
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

type updateGoalArguments struct {
	Status string `json:"status"`
}

func NewUpdateGoal(goal *agent.Goal) agent.Tool {
	return spec[updateGoalArguments]{
		definition: updateGoalToolDefinition,
		present: func(args updateGoalArguments) agent.ToolPresentation {
			return agent.ToolPresentation{Arguments: args.Status}
		},
		run: func(_ context.Context, args updateGoalArguments, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
			if args.Status != "complete" {
				return agent.ToolResult{}, errors.New(`status must be "complete"`)
			}
			switch err := goal.Complete(); {
			case errors.Is(err, agent.ErrNoGoal):
				return successResult("No active goal."), nil
			case errors.Is(err, agent.ErrGoalAlreadyComplete):
				return successResult("Goal already complete."), nil
			default:
				return successResult("Goal marked complete."), nil
			}
		},
	}
}
