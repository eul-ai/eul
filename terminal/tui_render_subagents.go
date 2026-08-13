package terminal

import (
	"fmt"
	"strings"
	"time"

	"github.com/eul-ai/eul/subagent"
)

func renderSubagents(model *tuiModel, height int) []styledLine {
	return renderSubagentsAt(model, height, time.Now())
}

func renderSubagentsAt(model *tuiModel, height int, now time.Time) []styledLine {
	lines := make([]styledLine, 0, min(height, len(model.subagentStatus.Active)+len(model.subagentStatus.PendingCompletions)))
	rows := subagentRows(model.subagentStatus)
	lines = appendSubagentSection(lines, "active", rows[:len(model.subagentStatus.Active)], height, now)
	awaiting := rows[len(model.subagentStatus.Active):]
	return appendSubagentSection(lines, "awaiting delivery", awaiting, height, now)
}

func appendSubagentSection(lines []styledLine, category string, jobs []subagentRow, height int, now time.Time) []styledLine {
	for _, job := range jobs {
		if len(lines) >= height {
			break
		}
		state := string(job.State)
		details := make([]string, 0, 4)
		if job.ModelProfile != "" {
			details = append(details, string(job.ModelProfile))
		}
		if job.ThinkingLevel != "" {
			details = append(details, string(job.ThinkingLevel)+" thinking")
		}
		details = append(details, subagentElapsed(job.Started, job.Finished, now))
		switch {
		case job.Usage.InputTokens > 0 || job.Usage.OutputTokens > 0:
			details = append(details, formatTokens(job.Usage.InputTokens)+" input", formatTokens(job.Usage.OutputTokens)+" output")
		case job.Usage.TotalTokens > 0:
			details = append(details, formatTokens(job.Usage.TotalTokens)+" processed")
		}
		if job.GenerationLimit > 0 {
			details = append(details, fmt.Sprintf("%d/%d generations", job.Generations, job.GenerationLimit))
		}

		spans := []inlineSpan{
			{text: category + " · ", style: inlineStyle{foreground: inlineForegroundDefault}},
			{text: job.ID, style: inlineStyle{bold: true}},
			{text: "  "},
			{text: state, style: inlineStyle{foreground: subagentStateForeground(job.State)}},
			{text: " (" + strings.Join(details, ", ") + ")"},
		}
		if job.Task != "" {
			spans = append(spans, inlineSpan{text: " — " + job.Task})
		}
		lines = append(lines, styledLine{
			spans:   spans,
			style:   lineStyle{foreground: currentTheme.muted},
			padding: 2,
		})
	}
	return lines
}

func subagentStateForeground(state subagent.State) inlineForeground {
	switch state {
	case subagent.StateRunning:
		return inlineForegroundAccent
	case subagent.StateFinalizing, subagent.StateCanceling, subagent.StateCanceled:
		return inlineForegroundOrange
	case subagent.StateComplete:
		return inlineForegroundSuccess
	case subagent.StateFailed, subagent.StateInterrupted:
		return inlineForegroundError
	default:
		return inlineForegroundDefault
	}
}

func subagentElapsed(started, finished, now time.Time) string {
	end := now
	if !finished.IsZero() {
		end = finished
	}
	elapsed := end.Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Truncate(time.Second).String()
}
