package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const maxToolPresentationSummaryBytes = 500

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

func sanitizeToolPresentation(presentation agent.ToolPresentation) agent.ToolPresentation {
	presentation.Title = singleLine(presentation.Title, maxToolPresentationSummaryBytes)
	presentation.Arguments = singleLine(presentation.Arguments, maxToolPresentationSummaryBytes)
	presentation.Outcome = singleLine(presentation.Outcome, maxToolPresentationSummaryBytes)
	return presentation
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

func replaceControls(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}

func singleLine(value string, maximum int) string {
	value = strings.Join(strings.Fields(replaceControls(value)), " ")

	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}
