package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpdateGoalMarksActiveGoalComplete(t *testing.T) {
	calls := 0
	goalTool := NewUpdateGoal(func() error {
		calls++
		return nil
	})
	if description := goalTool.Definition().Description; description != "Mark an active goal complete only when all requirements are verified." {
		t.Fatalf("description = %q", description)
	}

	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Output != "Goal marked complete." || calls != 1 {
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
	if !result.IsError || !strings.Contains(result.Output, `status must be "complete"`) || calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestUpdateGoalReportsInactiveGoal(t *testing.T) {
	inactive := errors.New("agent: no goal is set")
	goalTool := NewUpdateGoal(func() error { return inactive })

	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, inactive.Error()) {
		t.Fatalf("result = %+v", result)
	}
}
