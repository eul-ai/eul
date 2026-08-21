package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

const goalContinuationPrompt = `Continue the active goal. Take the next unfinished step, or call update_goal when complete.`

type steeringSignalKey struct{}

func SteeringSignal(ctx context.Context) <-chan struct{} {
	signal, _ := ctx.Value(steeringSignalKey{}).(<-chan struct{})
	return signal
}

type continuationPoint uint8

const (
	continuationAfterToolBatch continuationPoint = iota
	continuationBeforeSettle
)

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

func cloneContentBatches(batches [][]ContentPart) [][]ContentPart {
	cloned := make([][]ContentPart, len(batches))
	for index, content := range batches {
		cloned[index] = cloneContentParts(content)
	}
	return cloned
}

func (arbiter *continuationArbiter) beginRun() {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
	arbiter.acceptingSteering = true
}

func (arbiter *continuationArbiter) endRun() {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
	arbiter.acceptingSteering = false
}

func (arbiter *continuationArbiter) steer(content []ContentPart) bool {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if !arbiter.acceptingSteering {
		return false
	}
	arbiter.steering = append(arbiter.steering, cloneContentParts(content))
	if arbiter.toolRoundSteering != nil {
		close(arbiter.toolRoundSteering)
		arbiter.toolRoundSteering = nil
	}
	return true
}

func (arbiter *continuationArbiter) clearSteering() [][]ContentPart {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	steering := cloneContentBatches(arbiter.steering)
	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
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

	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
	arbiter.acceptingSteering = false
	arbiter.goal = nil
	if goal != nil {
		restored := *goal
		arbiter.goal = &restored
	}
}

func (arbiter *continuationArbiter) completeGoal() error {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	switch {
	case arbiter.goal == nil:
		return errors.New("agent: no goal is set")
	case arbiter.goal.Complete:
		return errors.New("agent: goal is already complete")
	default:
		arbiter.goal.Complete = true
		return nil
	}
}

func (arbiter *continuationArbiter) reset() {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	arbiter.steering = nil
	arbiter.toolRoundSteering = nil
	arbiter.acceptingSteering = false
	arbiter.goal = nil
}

func (arbiter *continuationArbiter) next(point continuationPoint) (pendingContinuation, bool) {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()

	if len(arbiter.steering) > 0 {
		content := arbiter.steering[0]
		arbiter.steering[0] = nil
		arbiter.steering = arbiter.steering[1:]
		return pendingContinuation{kind: continuationSteering, content: content}, true
	}

	if point == continuationBeforeSettle && arbiter.goal != nil && !arbiter.goal.Complete {
		return pendingContinuation{
			kind: continuationGoal,
			content: []ContentPart{{
				Kind: ContentPartText,
				Text: goalContinuationPrompt + "\n\nGoal: " + arbiter.goal.Objective,
			}},
		}, true
	}

	if point == continuationBeforeSettle {
		arbiter.acceptingSteering = false
	}
	return pendingContinuation{}, false
}
