package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

const goalContinuationPrompt = `Continue the active goal. Take the next unfinished step, or call update_goal when complete.`

var (
	ErrNoGoal              = errors.New("agent: no goal is set")
	ErrGoalAlreadyComplete = errors.New("agent: goal is already complete")
)

type steeringSignalKey struct{}

func SteeringSignal(ctx context.Context) <-chan struct{} {
	signal, _ := ctx.Value(steeringSignalKey{}).(<-chan struct{})
	return signal
}

type continuationKind uint8

const (
	continuationSteering continuationKind = iota
	continuationGoal
)

type pendingContinuation struct {
	kind    continuationKind
	content []ContentPart
}

type GoalState struct {
	Objective string `json:"objective"`
	Complete  bool   `json:"complete"`
}

type continuationArbiter struct {
	mu                sync.Mutex
	acceptingSteering bool
	steering          [][]ContentPart
	toolRoundSteering chan struct{}
	goal              *GoalState
}

func (arbiter *continuationArbiter) setRunning(running bool) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
	arbiter.acceptingSteering = running
}

func (arbiter *continuationArbiter) steer(content []ContentPart) bool {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if !arbiter.acceptingSteering {
		return false
	}
	arbiter.steering = append(arbiter.steering, content)
	if arbiter.toolRoundSteering != nil {
		close(arbiter.toolRoundSteering)
		arbiter.toolRoundSteering = nil
	}
	return true
}

func (arbiter *continuationArbiter) clearSteering() [][]ContentPart {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	steering := arbiter.steering
	arbiter.steering = nil
	return steering
}

func (arbiter *continuationArbiter) beginToolRound() <-chan struct{} {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	steering := make(chan struct{})
	if len(arbiter.steering) > 0 {
		close(steering)
		return steering
	}
	arbiter.toolRoundSteering = steering
	return steering
}

func (arbiter *continuationArbiter) endToolRound(steering <-chan struct{}) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if arbiter.toolRoundSteering == steering {
		arbiter.toolRoundSteering = nil
	}
}

func (arbiter *continuationArbiter) setGoal(objective string) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("agent: goal objective is required")
	}

	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.goal = &GoalState{Objective: objective}
	return nil
}

func (arbiter *continuationArbiter) getGoal() (GoalState, bool) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if arbiter.goal == nil {
		return GoalState{}, false
	}
	return *arbiter.goal, true
}

func (arbiter *continuationArbiter) clearGoal() {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.goal = nil
}

func (arbiter *continuationArbiter) restoreGoal(goal *GoalState) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.goal = goal
}

func (arbiter *continuationArbiter) completeGoal() error {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	switch {
	case arbiter.goal == nil:
		return ErrNoGoal
	case arbiter.goal.Complete:
		return ErrGoalAlreadyComplete
	default:
		arbiter.goal.Complete = true
		return nil
	}
}

func (arbiter *continuationArbiter) nextAfterTools() (pendingContinuation, bool) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	return arbiter.nextSteeringLocked()
}

func (arbiter *continuationArbiter) nextBeforeSettle() (pendingContinuation, bool) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if next, ok := arbiter.nextSteeringLocked(); ok {
		return next, true
	}
	if arbiter.goal != nil && !arbiter.goal.Complete {
		return pendingContinuation{
			kind: continuationGoal,
			content: []ContentPart{{
				Kind: ContentPartText,
				Text: goalContinuationPrompt + "\n\nGoal: " + arbiter.goal.Objective,
			}},
		}, true
	}
	arbiter.acceptingSteering = false
	return pendingContinuation{}, false
}

func (arbiter *continuationArbiter) nextSteeringLocked() (pendingContinuation, bool) {
	if len(arbiter.steering) == 0 {
		return pendingContinuation{}, false
	}

	content := arbiter.steering[0]
	arbiter.steering[0] = nil
	arbiter.steering = arbiter.steering[1:]
	return pendingContinuation{kind: continuationSteering, content: content}, true
}
