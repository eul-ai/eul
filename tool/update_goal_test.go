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

func TestUpdateGoalReportsInactiveGoal(t *testing.T) {
	inactive := errors.New("inactive sentinel")
	goalTool := NewUpdateGoal(func() error { return inactive })

	result, err := goalTool.Execute(context.Background(), []byte(`{"status":"complete"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, inactive.Error()) {
		t.Fatalf("result = %+v", result)
	}
}
