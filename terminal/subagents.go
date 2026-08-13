package terminal

import (
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

type subagentRow struct {
	ID            string
	Task          string
	ModelProfile  subagent.Profile
	ThinkingLevel agent.ThinkingLevel
	State         subagent.State
	Started       time.Time
	Finished      time.Time
	Usage         agent.Usage
}

func subagentRows(status subagent.Status) []subagentRow {
	rows := make([]subagentRow, 0, len(status.Active)+len(status.PendingCompletions))
	for _, job := range status.Active {
		rows = append(rows, subagentRow{
			ID:            job.ID,
			Task:          job.Task,
			ModelProfile:  job.ModelProfile,
			ThinkingLevel: job.ThinkingLevel,
			State:         job.State,
			Started:       job.Started,
			Usage:         job.Usage,
		})
	}
	for _, completion := range status.PendingCompletions {
		rows = append(rows, subagentRow{
			ID:       completion.SubagentID,
			Task:     completion.Task,
			State:    completion.Status,
			Started:  completion.Started,
			Finished: completion.Finished,
		})
	}
	return rows
}
