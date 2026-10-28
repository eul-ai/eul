package terminal

import (
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/text"
)

type subagentRow struct {
	ID            string
	Task          string
	ModelProfile  subagent.Profile
	ThinkingLevel agent.ThinkingLevel
	State         subagent.State
	Started       time.Time
	Finished      time.Time
	Usage         agent.Usage
}

func subagentRows(status subagent.Status) []subagentRow {
	rows := make([]subagentRow, 0, len(status.Active)+len(status.PendingCompletions))
	for _, job := range status.Active {
		rows = append(rows, subagentRow{
			ID:            job.ID,
			Task:          job.Task,
			ModelProfile:  job.ModelProfile,
			ThinkingLevel: job.ThinkingLevel,
			State:         job.State,
			Started:       job.Started,
			Usage:         job.Usage,
		})
	}
	for _, completion := range status.PendingCompletions {
		rows = append(rows, subagentRow{
			ID:       completion.SubagentID,
			Task:     completion.Task,
			State:    completion.Status,
			Started:  completion.Started,
			Finished: completion.Finished,
		})
	}
	return rows
}

func renderSubagents(model *tuiModel, height int) []styledLine {
	now := time.Now()
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
		spans := []text.Span{
			{Text: category + " · ", Style: text.Style{Foreground: text.ForegroundDefault}},
			{Text: job.ID, Style: text.Style{Bold: true}},
			{Text: "  "},
			{Text: state, Style: text.Style{Foreground: subagentStateForeground(job.State)}},
			{Text: " (" + strings.Join(details, ", ") + ")"},
		}
		if job.Task != "" {
			spans = append(spans, text.Span{Text: " — " + job.Task})
		}
		lines = append(lines, styledLine{
			spans:   spans,
			style:   lineStyle{foreground: currentTheme.muted},
			padding: 1,
		})
	}
	return lines
}

func subagentStateForeground(state subagent.State) text.Foreground {
	switch state {
	case subagent.StateRunning:
		return text.ForegroundAccent
	case subagent.StateCanceling, subagent.StateCanceled:
		return text.ForegroundOrange
	case subagent.StateComplete:
		return text.ForegroundSuccess
	case subagent.StateFailed, subagent.StateInterrupted:
		return text.ForegroundError
	default:
		return text.ForegroundDefault
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
