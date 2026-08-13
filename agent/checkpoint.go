package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const checkpointVersion = 2

type Checkpoint struct {
	data checkpointData
}

type checkpointData struct {
	Version       int        `json:"version"`
	State         []byte     `json:"state,omitempty"`
	ContextUsage  Usage      `json:"context_usage"`
	PendingInputs []Input    `json:"pending_inputs,omitempty"`
	Goal          *GoalState `json:"goal,omitempty"`
}

func (checkpoint Checkpoint) Initialized() bool {
	return checkpoint.data.Version == checkpointVersion
}

func (checkpoint Checkpoint) MarshalJSON() ([]byte, error) {
	data := cloneCheckpointData(checkpoint.data)
	if err := validateCheckpointData(data); err != nil {
		return nil, err
	}
	return json.Marshal(data)
}

func (checkpoint *Checkpoint) UnmarshalJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()

	var data checkpointData
	if err := decoder.Decode(&data); err != nil {
		return fmt.Errorf("agent: decode checkpoint: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent: decode checkpoint: multiple JSON values")
		}
		return fmt.Errorf("agent: decode checkpoint: %w", err)
	}
	if err := validateCheckpointData(data); err != nil {
		return err
	}

	checkpoint.data = cloneCheckpointData(data)
	return nil
}

func validateCheckpointData(data checkpointData) error {
	if data.Version != checkpointVersion {
		return fmt.Errorf("agent: unsupported checkpoint version %d", data.Version)
	}
	if data.ContextUsage.InputTokens < 0 || data.ContextUsage.OutputTokens < 0 || data.ContextUsage.TotalTokens < 0 {
		return errors.New("agent: checkpoint usage contains negative token counts")
	}
	for index, input := range data.PendingInputs {
		if input.Kind == InputInbox {
			return fmt.Errorf("agent: checkpoint input %d contains transient inbox data", index)
		}
		if err := input.Validate(); err != nil {
			return fmt.Errorf("agent: checkpoint input %d: %w", index, err)
		}
	}
	if data.Goal != nil && strings.TrimSpace(data.Goal.Objective) == "" {
		return errors.New("agent: checkpoint goal objective is empty")
	}
	return nil
}

func cloneCheckpointData(data checkpointData) checkpointData {
	conversation := conversationState{state: data.State, inputs: data.PendingInputs}.clone()
	data.State = conversation.state
	data.PendingInputs = conversation.inputs
	if data.Goal != nil {
		goal := *data.Goal
		data.Goal = &goal
	}
	return data
}

func (e *Engine) Checkpoint() (Checkpoint, error) {
	if !e.mu.TryLock() {
		return Checkpoint{}, errEngineBusy
	}
	defer e.mu.Unlock()

	return e.checkpointLocked(), nil
}

func (e *Engine) RestoreCheckpoint(checkpoint Checkpoint) error {
	if !e.mu.TryLock() {
		return errEngineBusy
	}
	defer e.mu.Unlock()

	data := cloneCheckpointData(checkpoint.data)
	if data.Version == 0 {
		return errors.New("agent: checkpoint is uninitialized")
	}
	if err := validateCheckpointData(data); err != nil {
		return err
	}

	e.conversation = conversationState{
		state:  data.State,
		usage:  data.ContextUsage,
		inputs: data.PendingInputs,
	}
	e.continuations.restoreGoal(data.Goal)
	return nil
}

func (e *Engine) checkpointLocked() Checkpoint {
	goal, ok := e.continuations.getGoal()
	var storedGoal *GoalState
	if ok {
		storedGoal = &goal
	}

	conversation := e.conversation.clone()
	return Checkpoint{data: checkpointData{
		Version:       checkpointVersion,
		State:         conversation.state,
		ContextUsage:  conversation.usage,
		PendingInputs: conversation.inputs,
		Goal:          storedGoal,
	}}
}

func (e *Engine) commitCheckpoint(current conversationState, sink EventSink) error {
	current.checkpoint(e)
	if !e.checkpointing {
		return nil
	}

	checkpoint := e.checkpointLocked()
	return emit(sink, Event{Kind: EventCheckpoint, Checkpoint: &checkpoint})
}
