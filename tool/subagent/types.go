package subagent

import (
	"context"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	MaxActive       = 4
	GenerationLimit = 20
)

type Profile string

const (
	ProfileFast     Profile = "fast"
	ProfileBalanced Profile = "balanced"
	ProfilePowerful Profile = "powerful"
)

type State string

const (
	StateRunning     State = "running"
	StateFinalizing  State = "finalizing"
	StateCanceling   State = "canceling"
	StateComplete    State = "complete"
	StateFailed      State = "failed"
	StateCanceled    State = "canceled"
	StateInterrupted State = "interrupted"
)

type Progress struct {
	Usage       agent.Usage
	Generations int
	Finalizing  bool
}

type Run func(context.Context, string, Profile, agent.ThinkingLevel, func(Progress)) (agent.RunResult, error)

type JobStatus struct {
	ID              string
	Task            string
	ModelProfile    Profile
	ThinkingLevel   agent.ThinkingLevel
	State           State
	Started         time.Time
	Usage           agent.Usage
	Generations     int
	GenerationLimit int
}

type Completion struct {
	MessageID  uint64    `json:"message_id"`
	SubagentID string    `json:"subagent_id"`
	Task       string    `json:"task"`
	Status     State     `json:"status"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Result     string    `json:"result,omitempty"`
}

type Status struct {
	Running    int
	Finalizing int
	Active     []JobStatus
	Awaiting   []Completion
}
