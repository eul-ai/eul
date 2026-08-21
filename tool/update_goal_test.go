package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestUpdateGoalMarksActiveGoalComplete(t *testing.T) {
	calls := 0
	goalTool := NewUpdateGoal(func() error {
		calls++
		return nil
	})
	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestUpdateGoalRejectsInvalidStatus(t *testing.T) {
	calls := 0
	goalTool := NewUpdateGoal(func() error {
		calls++
		return nil
	})

	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"paused"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestUpdateGoalIgnoresInactiveGoal(t *testing.T) {
	for _, completionErr := range []error{agent.ErrNoGoal, agent.ErrGoalAlreadyComplete} {
		goalTool := NewUpdateGoal(func() error { return completionErr })

		result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("completion error %v returned %+v", completionErr, result)
		}
	}
}

func TestUpdateGoalReportsCompletionFailure(t *testing.T) {
	failure := errors.New("completion failed")
	goalTool := NewUpdateGoal(func() error { return failure })

	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, failure.Error()) {
		t.Fatalf("result = %+v", result)
	}
}
