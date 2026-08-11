package terminal

import (
	"fmt"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
)

func renderSubagents(model *tuiModel, height int) []styledLine {
	return renderSubagentsAt(model, height, time.Now())
}

func renderSubagentsAt(model *tuiModel, height int, now time.Time) []styledLine {
	jobs := model.subagentStatus.Jobs
	lines := make([]styledLine, 0, min(height, len(jobs)))
	for _, job := range jobs[:min(height, len(jobs))] {
		state := string(job.State)
		if reason := subagentFinalizationReason(job.FinalizationReason); reason != "" && job.State == agent.SubagentFinalizing {
			state += " — " + reason
		}

		details := []string{subagentElapsed(job.Started, now)}
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
			{text: job.ID, style: inlineStyle{bold: true}},
			{text: "  "},
			{text: state, style: inlineStyle{foreground: inlineForegroundAccent}},
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

func subagentElapsed(started, now time.Time) string {
	elapsed := now.Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Truncate(time.Second).String()
}

func subagentFinalizationReason(reason agent.FinalizationReason) string {
	switch reason {
	case agent.FinalizationReasonDuration:
		return "time limit"
	case agent.FinalizationReasonGenerations:
		return "generation limit"
	default:
		return ""
	}
}
