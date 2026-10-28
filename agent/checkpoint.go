package agent

import "slices"

type Checkpoint struct {
	State         []byte     `json:"state,omitempty"`
	ContextUsage  Usage      `json:"context_usage"`
	PendingInputs []Input    `json:"pending_inputs,omitempty"`
	Goal          *GoalState `json:"goal,omitempty"`
}

func (e *Engine) Checkpoint() (Checkpoint, error) {
	if !e.mu.TryLock() {
		return Checkpoint{}, errEngineBusy
	}
	defer e.mu.Unlock()

	return e.checkpointLocked(), nil
}

func (e *Engine) checkpointLocked() Checkpoint {
	checkpoint := Checkpoint{
		State:         e.conversation.state,
		ContextUsage:  e.conversation.usage,
		PendingInputs: slices.Clip(e.conversation.inputs),
	}
	if goal, ok := e.continuations.goal.State(); ok {
		checkpoint.Goal = &goal
	}
	return checkpoint
}
