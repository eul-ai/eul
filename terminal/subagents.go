package terminal

import (
	"time"

	"github.com/eul-ai/eul/agent"
)

type SubagentState string

const (
	SubagentRunning     SubagentState = "running"
	SubagentFinalizing  SubagentState = "finalizing"
	SubagentCanceling   SubagentState = "canceling"
	SubagentComplete    SubagentState = "complete"
	SubagentFailed      SubagentState = "failed"
	SubagentCanceled    SubagentState = "canceled"
	SubagentInterrupted SubagentState = "interrupted"
)

type SubagentJobStatus struct {
	ID              string
	Task            string
	ModelProfile    string
	ThinkingLevel   agent.ThinkingLevel
	State           SubagentState
	Started         time.Time
	Finished        time.Time
	Usage           agent.Usage
	Generations     int
	GenerationLimit int
}

type SubagentCompletionStatus struct {
	MessageID  uint64
	SubagentID string
	Task       string
	State      SubagentState
	Started    time.Time
	Finished   time.Time
}

type SubagentStatus struct {
	Running    int
	Finalizing int
	Active     []SubagentJobStatus
	Awaiting   []SubagentCompletionStatus
}

func (status SubagentStatus) jobs() []SubagentJobStatus {
	jobs := append([]SubagentJobStatus(nil), status.Active...)
	for _, completion := range status.Awaiting {
		jobs = append(jobs, SubagentJobStatus{
			ID:       completion.SubagentID,
			Task:     completion.Task,
			State:    completion.State,
			Started:  completion.Started,
			Finished: completion.Finished,
		})
	}
	return jobs
}
