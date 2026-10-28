package tool

import (
	"context"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestUpdateGoalMarksActiveGoalComplete(t *testing.T) {
	goal := &agent.Goal{}
	if err := goal.Set("finish"); err != nil {
		t.Fatal(err)
	}
	result, err := NewUpdateGoal(goal).Execute(context.Background(), []byte(`{"status":"complete"}`), discardUpdates)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := goal.State()
	if result.IsError || !state.Complete {
		t.Fatalf("result=%+v goal=%+v", result, state)
	}
}

func TestUpdateGoalRejectsInvalidStatus(t *testing.T) {
	goal := &agent.Goal{}
	if err := goal.Set("finish"); err != nil {
		t.Fatal(err)
	}

	result, err := NewUpdateGoal(goal).Execute(context.Background(), []byte(`{"status":"paused"}`), discardUpdates)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := goal.State()
	if !result.IsError || state.Complete {
		t.Fatalf("result=%+v goal=%+v", result, state)
	}
}

func TestUpdateGoalIgnoresInactiveGoal(t *testing.T) {
	complete := &agent.Goal{}
	if err := complete.Set("finish"); err != nil {
		t.Fatal(err)
	}
	if err := complete.Complete(); err != nil {
		t.Fatal(err)
	}

	for _, goal := range []*agent.Goal{{}, complete} {
		result, err := NewUpdateGoal(goal).Execute(context.Background(), []byte(`{"status":"complete"}`), discardUpdates)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("inactive goal returned %+v", result)
		}
	}
}
