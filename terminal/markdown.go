package terminal

import (
	"strings"
	"unicode/utf8"
)

type inlineStyle struct {
	bold   bool
	italic bool
}

type inlineSpan struct {
	text  string
	style inlineStyle
}

type formattedLine struct {
	text  string
	spans []inlineSpan
}

func wrapInlineMarkdown(text string, width int) []formattedLine {
	if width <= 0 {
		return nil
	}

	spans := parseInlineMarkdown(text)
	lines := make([]formattedLine, 0, 1)
	current := make([]inlineSpan, 0, 1)
	lineWidth := 0
	flush := func() {
		lines = append(lines, formattedLine{text: inlineSpanText(current), spans: current})
		current = nil
		lineWidth = 0
	}
	appendCharacter := func(character rune, style inlineStyle) {
		characterWidth := runeWidth(character)
		if lineWidth > 0 && lineWidth+characterWidth > width {
			flush()
		}
		appendInlineSpan(&current, string(character), style)
		lineWidth += characterWidth
	}

	for _, span := range spans {
		for _, character := range span.text {
			switch character {
			case '\n':
				flush()
			case '\t':
				for range 4 {
					appendCharacter(' ', span.style)
				}
			default:
				appendCharacter(character, span.style)
			}
		}
	}
	flush()
	return lines
}

func parseInlineMarkdown(text string) []inlineSpan {
	var spans []inlineSpan
	for index := 0; index < len(text); {
		delimiter := ""
		style := inlineStyle{}
		switch {
		case strings.HasPrefix(text[index:], "***"):
			delimiter = "***"
			style = inlineStyle{bold: true, italic: true}
		case strings.HasPrefix(text[index:], "**"):
			delimiter = "**"
			style = inlineStyle{bold: true}
		case text[index] == '*':
			delimiter = "*"
			style = inlineStyle{italic: true}
		}
		if delimiter != "" {
			contentStart := index + len(delimiter)
			closing := strings.Index(text[contentStart:], delimiter)
			if closing > 0 {
				contentEnd := contentStart + closing
				appendInlineSpan(&spans, text[contentStart:contentEnd], style)
				index = contentEnd + len(delimiter)
				continue
			}
			appendInlineSpan(&spans, delimiter, inlineStyle{})
			index += len(delimiter)
			continue
		}

		_, size := utf8.DecodeRuneInString(text[index:])
		appendInlineSpan(&spans, text[index:index+size], inlineStyle{})
		index += size
	}
	return spans
}

func truncateInlineSpans(spans []inlineSpan, width int) []inlineSpan {
	if width <= 0 {
		return nil
	}

	var result []inlineSpan
	used := 0
truncate:
	for _, span := range spans {
		for _, character := range span.text {
			characterWidth := runeWidth(character)
			if used+characterWidth > width {
				break truncate
			}
			appendInlineSpan(&result, string(character), span.style)
			used += characterWidth
		}
	}
	return result
}

func appendInlineSpan(spans *[]inlineSpan, text string, style inlineStyle) {
	if text == "" {
		return
	}
	if len(*spans) > 0 && (*spans)[len(*spans)-1].style == style {
		(*spans)[len(*spans)-1].text += text
		return
	}
	*spans = append(*spans, inlineSpan{text: text, style: style})
}

func inlineSpanText(spans []inlineSpan) string {
	var text strings.Builder
	for _, span := range spans {
		text.WriteString(span.text)
	}
	return text.String()
}
