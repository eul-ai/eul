package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
)

const maxToolPresentationSummaryBytes = 500

func sanitizeEntry(entry session.Entry) session.Entry {
	entry.Text = sanitizeAssistantText(entry.Text)
	entry.ToolName = singleLine(entry.ToolName, maxToolPresentationSummaryBytes)
	entry.Tool.Arguments = singleLine(entry.Tool.Arguments, maxToolPresentationSummaryBytes)
	entry.Tool.Outcome = singleLine(entry.Tool.Outcome, maxToolPresentationSummaryBytes)
	content := make([]agent.ContentPart, len(entry.Content))
	for index, part := range entry.Content {
		part.Text = sanitizeAssistantText(part.Text)
		content[index] = part
	}
	entry.Content = content
	return entry
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
