package subagent

import (
	"context"
	"time"

	"github.com/eul-ai/eul/agent"
)

const maxActive = 4

type Profile string

const (
	ProfileFast     Profile = "fast"
	ProfileBalanced Profile = "balanced"
	ProfileMain     Profile = "main"
)

func (profile Profile) valid() bool {
	switch profile {
	case ProfileFast, ProfileBalanced, ProfileMain:
		return true
	default:
		return false
	}
}

type State string

const (
	StateRunning     State = "running"
	StateCanceling   State = "canceling"
	StateComplete    State = "complete"
	StateFailed      State = "failed"
	StateCanceled    State = "canceled"
	StateInterrupted State = "interrupted"
)

type Progress struct {
	Usage agent.Usage
}

type Task struct {
	Description   string
	Prompt        string
	ModelProfile  Profile
	ThinkingLevel agent.ThinkingLevel
}

type Job struct {
	ID            string
	Description   string
	ModelProfile  Profile
	ThinkingLevel agent.ThinkingLevel
}

type WaitOutcome uint8

const (
	WaitCompletion WaitOutcome = iota
	WaitSteering
	WaitTimeout
)

type RunRequest struct {
	Task          string
	Profile       Profile
	ThinkingLevel agent.ThinkingLevel
}

type Runner interface {
	Run(context.Context, RunRequest, func(Progress)) (agent.RunResult, error)
}

type RunFunc func(context.Context, RunRequest, func(Progress)) (agent.RunResult, error)

func (run RunFunc) Run(ctx context.Context, request RunRequest, update func(Progress)) (agent.RunResult, error) {
	return run(ctx, request, update)
}

type Config struct {
	Runner                  Runner
	SupportedThinkingLevels func(Profile) []agent.ThinkingLevel
	DefaultThinkingLevel    func() agent.ThinkingLevel
}

type JobStatus struct {
	ID            string
	Task          string
	ModelProfile  Profile
	ThinkingLevel agent.ThinkingLevel
	State         State
	Started       time.Time
	Usage         agent.Usage
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
	Running            int
	Active             []JobStatus
	PendingCompletions []Completion
}
