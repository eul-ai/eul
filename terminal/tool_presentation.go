package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const maxToolPresentationSummaryBytes = 500

func toolTitle(call agent.ToolCall, presentation agent.ToolPresentation) string {
	if title := strings.TrimSpace(presentation.Title); title != "" {
		return title
	}
	if call.Name != "" {
		return call.Name
	}
	return "tool"
}

func toolHeading(call agent.ToolCall, presentation agent.ToolPresentation) string {
	title := toolTitle(call, presentation)
	if arguments := strings.TrimSpace(presentation.Arguments); arguments != "" {
		title += " " + arguments
	}
	return title
}

func toolActivityDetail(call agent.ToolCall, presentation agent.ToolPresentation) string {
	if call.Name == "bash" || toolTitle(call, presentation) == "bash" {
		return "bash"
	}
	return diagnostic(toolHeading(call, presentation), maxToolPresentationSummaryBytes)
}

func toolResultOutcome(result agent.ToolResult, presentation agent.ToolPresentation) string {
	if presentation.Outcome != "" {
		return presentation.Outcome
	}
	if !result.IsError {
		return "ok"
	}
	detail := result.Output
	if newline := strings.IndexByte(detail, '\n'); newline >= 0 {
		detail = detail[:newline]
	}
	if detail == "" {
		return "error"
	}
	return "error: " + detail
}

func sanitizeToolPresentation(call agent.ToolCall, presentation agent.ToolPresentation) agent.ToolPresentation {
	presentation = presentation.Clone()
	presentation.Title = diagnostic(toolTitle(call, presentation), maxToolPresentationSummaryBytes)
	presentation.Arguments = diagnostic(presentation.Arguments, maxToolPresentationSummaryBytes)
	presentation.Outcome = diagnostic(presentation.Outcome, maxToolPresentationSummaryBytes)
	presentation.TailLines = max(0, presentation.TailLines)
	presentation.Elapsed = max(0, presentation.Elapsed)
	presentation.Timeout = max(0, presentation.Timeout)
	for index := range presentation.Lines {
		presentation.Lines[index] = sanitizeAssistantText(presentation.Lines[index])
	}
	for index := range presentation.Diff {
		presentation.Diff[index].Text = sanitizeAssistantText(presentation.Diff[index].Text)
	}
	return presentation
}

func diagnostic(value string, maximum int) string {
	return singleLine(value, maximum)
}

func sanitizeAssistantText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func singleLine(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")

	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
