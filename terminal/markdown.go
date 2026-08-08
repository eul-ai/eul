package terminal

import (
	"strings"
	"unicode/utf8"
)

type inlineForeground uint8

const (
	inlineForegroundDefault inlineForeground = iota
	inlineForegroundAccent
	inlineForegroundError
)

type inlineStyle struct {
	bold       bool
	italic     bool
	code       bool
	foreground inlineForeground
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
	return wrapInlineSpans(parseInlineMarkdown(text), width)
}

func wrapInlineSpans(spans []inlineSpan, width int) []formattedLine {
	if width <= 0 {
		return nil
	}

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
	return parseInlineMarkdownStyle(text, inlineStyle{})
}

func parseInlineMarkdownStyle(text string, inherited inlineStyle) []inlineSpan {
	var spans []inlineSpan
	for index := 0; index < len(text); {
		delimiter := ""
		style := inlineStyle{}
		switch {
		case text[index] == '`':
			end := index
			for end < len(text) && text[end] == '`' {
				end++
			}
			if end-index >= 3 {
				appendInlineSpan(&spans, text[index:end], inherited)
				index = end
				continue
			}
			delimiter = text[index:end]
			style = inlineStyle{code: true}
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
				content := text[contentStart:contentEnd]
				if style.code {
					appendInlineSpan(&spans, content, mergeInlineStyles(inherited, style))
				} else {
					for _, span := range parseInlineMarkdownStyle(content, mergeInlineStyles(inherited, style)) {
						appendInlineSpan(&spans, span.text, span.style)
					}
				}
				index = contentEnd + len(delimiter)
				continue
			}
			appendInlineSpan(&spans, delimiter, inherited)
			index += len(delimiter)
			continue
		}

		_, size := utf8.DecodeRuneInString(text[index:])
		appendInlineSpan(&spans, text[index:index+size], inherited)
		index += size
	}
	return spans
}

func mergeInlineStyles(left, right inlineStyle) inlineStyle {
	foreground := left.foreground
	if right.foreground != inlineForegroundDefault {
		foreground = right.foreground
	}
	return inlineStyle{
		bold:       left.bold || right.bold,
		italic:     left.italic || right.italic,
		code:       left.code || right.code,
		foreground: foreground,
	}
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
