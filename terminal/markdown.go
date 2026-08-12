package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type inlineForeground uint8

const (
	inlineForegroundDefault inlineForeground = iota
	inlineForegroundAccent
	inlineForegroundOrange
	inlineForegroundSuccess
	inlineForegroundError
)

type inlineStyle struct {
	bold       bool
	italic     bool
	code       bool
	foreground inlineForeground
	link       string
}

type inlineSpan struct {
	text  string
	style inlineStyle
}

type formattedLine struct {
	text         string
	spans        []inlineSpan
	continuation bool
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
	continuation := false
	flush := func() {
		lines = append(lines, formattedLine{text: inlineSpanText(current), spans: current, continuation: continuation})
		current = nil
		lineWidth = 0
	}
	appendCharacter := func(character rune, style inlineStyle) {
		characterWidth := runeWidth(character)
		if lineWidth > 0 && lineWidth+characterWidth > width {
			flush()
			continuation = true
		}
		appendInlineSpan(&current, string(character), style)
		lineWidth += characterWidth
	}

	for _, span := range spans {
		for _, character := range span.text {
			switch character {
			case '\n':
				flush()
				continuation = false
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
		if inherited.link == "" {
			if label, destination, consumed, ok := parseMarkdownLink(text[index:]); ok {
				style := inherited
				style.link = destination
				for _, span := range parseInlineMarkdownStyle(label, style) {
					appendInlineSpan(&spans, span.text, span.style)
				}
				index += consumed
				continue
			}
			if destination, consumed, ok := parseAutolink(text[index:]); ok {
				style := inherited
				style.link = destination
				appendInlineSpan(&spans, destination, style)
				index += consumed
				continue
			}
		}

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

func parseMarkdownLink(text string) (string, string, int, bool) {
	if !strings.HasPrefix(text, "[") {
		return "", "", 0, false
	}
	labelEnd := strings.Index(text, "](")
	if labelEnd <= 1 {
		return "", "", 0, false
	}
	destinationStart := labelEnd + 2
	destinationEnd := markdownDestinationEnd(text, destinationStart)
	if destinationEnd <= destinationStart {
		return "", "", 0, false
	}
	destination := text[destinationStart:destinationEnd]
	if !clickableURL(destination) {
		return "", "", 0, false
	}
	return text[1:labelEnd], destination, destinationEnd + 1, true
}

func markdownDestinationEnd(text string, start int) int {
	depth := 0
	for index := start; index < len(text); index++ {
		switch text[index] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return index
			}
			depth--
		}
	}
	return -1
}

func parseAutolink(text string) (string, int, bool) {
	if strings.HasPrefix(text, "<") {
		end := strings.IndexByte(text, '>')
		if end < 0 || !clickableURL(text[1:end]) {
			return "", 0, false
		}
		return text[1:end], end + 1, true
	}
	if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		return "", 0, false
	}

	end := 0
	for end < len(text) {
		character, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(character) {
			break
		}
		end += size
	}
	end = bareURLDestinationEnd(text, end)
	return text[:end], end, true
}

func bareURLDestinationEnd(text string, end int) int {
	for end > 0 {
		character := rune(text[end-1])
		switch {
		case strings.ContainsRune(".,;:!?]}>\"'*", character):
			end--
		case character == ')':
			if strings.Count(text[:end], ")") <= strings.Count(text[:end], "(") {
				return end
			}
			end--
		default:
			return end
		}
	}
	return end
}

func clickableURL(destination string) bool {
	if strings.ContainsFunc(destination, unicode.IsSpace) {
		return false
	}
	return strings.HasPrefix(destination, "http://") || strings.HasPrefix(destination, "https://") || strings.HasPrefix(destination, "mailto:")
}

func mergeInlineStyles(left, right inlineStyle) inlineStyle {
	foreground := left.foreground
	if right.foreground != inlineForegroundDefault {
		foreground = right.foreground
	}
	link := left.link
	if right.link != "" {
		link = right.link
	}
	return inlineStyle{
		bold:       left.bold || right.bold,
		italic:     left.italic || right.italic,
		code:       left.code || right.code,
		foreground: foreground,
		link:       link,
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
