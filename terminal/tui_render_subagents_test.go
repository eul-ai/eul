package terminal

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func TestRunningSubagentsRenderAboveInput(t *testing.T) {
	started := time.Unix(100, 0)
	model := newTUIModel(140, 10, Options{})
	model.subagentStatus = subagent.Status{Active: []subagent.JobStatus{
		{
			ID: "subagent-1", Task: "inspect layout", ModelProfile: "balanced", ThinkingLevel: agent.ThinkingLow,
			State: subagent.StateRunning, Started: started, Usage: agent.Usage{InputTokens: 1_200, OutputTokens: 34},
		},
		{
			ID: "subagent-2", Task: "review progress", State: subagent.StateCanceling, Started: started,
		},
	}}

	lines := renderSubagentsAt(model, 2, started.Add(time.Minute+5*time.Second))
	first := renderedLineText(lines[0], model.width)
	second := renderedLineText(lines[1], model.width)
	for _, want := range []string{"subagent-1", "inspect layout", string(subagent.StateRunning), "balanced", string(agent.ThinkingLow), "1m5s", "1.2k", "34"} {
		if !strings.Contains(first, want) {
			t.Fatalf("running line %q omits %q", first, want)
		}
	}
	for _, want := range []string{"subagent-2", "review progress", string(subagent.StateCanceling), "1m5s"} {
		if !strings.Contains(second, want) {
			t.Fatalf("canceling line %q omits %q", second, want)
		}
	}

	_, layout := modelInputLayout(model)
	if layout.subagentHeight != 2 || layout.subagentRow != layout.conversationHeight+1 || layout.topRuleRow != layout.subagentRow+layout.subagentHeight {
		t.Fatalf("layout = %+v", layout)
	}
	frame := buildTerminalFrame(model)
	if !strings.Contains(frame.plainRows[layout.subagentRow-1], "subagent-1") || frame.cursorRow != layout.inputRow {
		t.Fatalf("frame layout=%+v rows=%q", layout, frame.plainRows)
	}
}

func TestSubagentStatusesUseStateColorsAndFreezeCompletedElapsed(t *testing.T) {
	started := time.Unix(100, 0)
	finished := started.Add(5 * time.Second)
	model := newTUIModel(80, 10, Options{})
	model.subagentStatus.Active = []subagent.JobStatus{
		{ID: "running", State: subagent.StateRunning, Started: started},
		{ID: "canceling", State: subagent.StateCanceling, Started: started},
	}
	model.subagentStatus.PendingCompletions = []subagent.Completion{
		{SubagentID: "completion-id", Status: subagent.StateComplete, Started: started, Finished: finished},
		{SubagentID: "failed", Status: subagent.StateFailed, Started: started, Finished: finished},
	}

	lines := renderSubagentsAt(model, 4, started.Add(time.Minute))
	wantForegrounds := []inlineForeground{
		inlineForegroundAccent,
		inlineForegroundOrange,
		inlineForegroundSuccess,
		inlineForegroundError,
	}
	wantColors := []terminalColor{
		currentTheme.accent,
		currentTheme.orange,
		currentTheme.green,
		currentTheme.error,
	}
	for index, line := range lines {
		if line.spans[3].style.foreground != wantForegrounds[index] {
			t.Fatalf("line %d foreground = %v", index, line.spans[3].style.foreground)
		}
		var rendered strings.Builder
		renderLine(&rendered, 1, model.width, line)
		if !strings.Contains(rendered.String(), ansiForeground(wantColors[index])) {
			t.Fatalf("line %d did not use color %+v: %q", index, wantColors[index], rendered.String())
		}
	}
	complete := renderedLineText(lines[2], model.width)
	if !strings.Contains(complete, "completion-id") || !strings.Contains(complete, string(subagent.StateComplete)) || !strings.Contains(complete, "5s") {
		t.Fatalf("complete line = %q", complete)
	}
}

func TestSubagentPanelIsCappedOnSmallTerminals(t *testing.T) {
	model := newTUIModel(40, 8, Options{})
	model.subagentStatus.Active = make([]subagent.JobStatus, 4)
	for index := range model.subagentStatus.Active {
		model.subagentStatus.Active[index] = subagent.JobStatus{ID: "subagent-" + strconv.Itoa(index+1), State: subagent.StateRunning}
	}

	_, layout := modelInputLayout(model)
	if layout.subagentHeight != 3 || layout.conversationHeight != 1 || layout.inputHeight != 1 || layout.statusRow != 8 {
		t.Fatalf("layout = %+v", layout)
	}
}

func TestStatusOmitsBackgroundSubagents(t *testing.T) {
	model := newTUIModel(120, 12, Options{Config: Config{Model: "model"}})
	model.subagentStatus = subagent.Status{
		Running: 1,
		Active:  []subagent.JobStatus{{ID: "subagent-1", State: subagent.StateRunning}},
	}

	left, _ := renderStatus(model, model.width)
	if strings.Contains(left, "subagent-1") {
		t.Fatalf("status includes background subagent = %q", left)
	}

	model.activity = activity{kind: activityThinking}
	left, _ = renderStatus(model, model.width)
	if model.activity.kind != activityThinking || strings.Contains(left, "subagent-1") {
		t.Fatalf("status includes background subagent = %q", left)
	}
}
