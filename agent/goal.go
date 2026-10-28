package agent

import (
	"errors"
	"strings"
	"sync"
)

var (
	ErrNoGoal              = errors.New("agent: no goal is set")
	ErrGoalAlreadyComplete = errors.New("agent: goal is already complete")
)

type GoalState struct {
	Objective string `json:"objective"`
	Complete  bool   `json:"complete"`
}

type Goal struct {
	mu    sync.Mutex
	state *GoalState
}

func (goal *Goal) Set(objective string) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("agent: goal objective is required")
	}

	goal.mu.Lock()
	defer goal.mu.Unlock()

	goal.state = &GoalState{Objective: objective}
	return nil
}

func (goal *Goal) State() (GoalState, bool) {
	goal.mu.Lock()
	defer goal.mu.Unlock()

	if goal.state == nil {
		return GoalState{}, false
	}
	return *goal.state, true
}

func (goal *Goal) Clear() {
	goal.restore(nil)
}

func (goal *Goal) Complete() error {
	goal.mu.Lock()
	defer goal.mu.Unlock()

	switch {
	case goal.state == nil:
		return ErrNoGoal
	case goal.state.Complete:
		return ErrGoalAlreadyComplete
	default:
		goal.state.Complete = true
		return nil
	}
}

func (goal *Goal) restore(state *GoalState) {
	goal.mu.Lock()
	defer goal.mu.Unlock()

	goal.state = state
}
