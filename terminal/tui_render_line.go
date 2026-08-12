package terminal

import (
	"strconv"
	"strings"
	"unicode"
)

const (
	ansiReset           = "\x1b[0m"
	ansiBold            = "\x1b[1m"
	ansiNormalIntensity = "\x1b[22m"
	ansiItalic          = "\x1b[3m"
	ansiNotItalic       = "\x1b[23m"
	ansiReverse         = "\x1b[7m"
	ansiNotReverse      = "\x1b[27m"
	ansiLinkClose       = "\x1b]8;;\x1b\\"
)

type lineStyle struct {
	foreground      terminalColor
	background      terminalColor
	paintBackground bool
	bold            bool
	italic          bool
}

type styledLine struct {
	prefixText       string
	prefixForeground *terminalColor
	text             string
	rightText        string
	spans            []inlineSpan
	style            lineStyle
	padding          int
}

type fittedLine struct {
	leftPadding  int
	rightPadding int
	textWidth    int
	prefix       string
	text         string
	right        string
	spans        []inlineSpan
}

func fitLine(line styledLine, width int) fittedLine {
	leftPadding := min(line.padding, width)
	rightPadding := min(line.padding, width-leftPadding)
	contentWidth := width - leftPadding - rightPadding
	right := truncateCells(line.rightText, contentWidth, false)
	textWidth := contentWidth - cellWidth(right)
	prefix := truncateCells(line.prefixText, textWidth, false)
	remainingTextWidth := textWidth - cellWidth(prefix)
	text := truncateCells(line.text, remainingTextWidth, false)
	spans := truncateInlineSpans(line.spans, remainingTextWidth)
	if len(spans) > 0 {
		text = inlineSpanText(spans)
	}
	return fittedLine{
		leftPadding:  leftPadding,
		rightPadding: rightPadding,
		textWidth:    textWidth,
		prefix:       prefix,
		text:         text,
		right:        right,
		spans:        spans,
	}
}

func renderedLineText(line styledLine, width int) string {
	fitted := fitLine(line, width)
	return strings.Repeat(" ", fitted.leftPadding) + fitted.prefix + fitted.text +
		strings.Repeat(" ", fitted.textWidth-cellWidth(fitted.prefix)-cellWidth(fitted.text)) +
		fitted.right + strings.Repeat(" ", fitted.rightPadding)
}

func renderLine(frame *strings.Builder, row, width int, line styledLine) {
	style := line.style
	if style == (lineStyle{}) {
		style = lineStyle{foreground: currentTheme.foreground}
	}
	writeCursorPosition(frame, row, 1)
	frame.WriteString(ansiColors(style.foreground, style.background, style.paintBackground))
	if style.bold {
		frame.WriteString(ansiBold)
	}
	if style.italic {
		frame.WriteString(ansiItalic)
	}
	fitted := fitLine(line, width)

	frame.WriteString(strings.Repeat(" ", fitted.leftPadding))
	foreground := style.foreground
	if fitted.prefix != "" {
		if line.prefixForeground != nil {
			writeTextForeground(frame, *line.prefixForeground, &foreground)
		}
		frame.WriteString(fitted.prefix)
		writeTextForeground(frame, style.foreground, &foreground)
	}
	if len(fitted.spans) == 0 {
		frame.WriteString(fitted.text)
	} else {
		bold := style.bold
		italic := style.italic
		link := ""
		for _, span := range fitted.spans {
			spanForeground := style.foreground
			switch span.style.foreground {
			case inlineForegroundAccent:
				spanForeground = currentTheme.accent
			case inlineForegroundOrange:
				spanForeground = currentTheme.orange
			case inlineForegroundSuccess:
				spanForeground = currentTheme.green
			case inlineForegroundError:
				spanForeground = currentTheme.error
			}
			if span.style.code {
				spanForeground = currentTheme.markdownCode
			}
			writeTextForeground(frame, spanForeground, &foreground)
			writeTextAttributes(frame, style.bold || span.style.bold, style.italic || span.style.italic, &bold, &italic)
			writeLink(frame, span.style.link, &link)
			frame.WriteString(span.text)
		}
		writeLink(frame, "", &link)
		writeTextForeground(frame, style.foreground, &foreground)
		writeTextAttributes(frame, style.bold, style.italic, &bold, &italic)
	}
	frame.WriteString(strings.Repeat(" ", fitted.textWidth-cellWidth(fitted.prefix)-cellWidth(fitted.text)))
	frame.WriteString(fitted.right)
	frame.WriteString(strings.Repeat(" ", fitted.rightPadding))
	frame.WriteString(ansiReset)
}

func writeLink(output *strings.Builder, link string, current *string) {
	if link == *current {
		return
	}
	if *current != "" {
		output.WriteString(ansiLinkClose)
	}
	if link != "" {
		output.WriteString("\x1b]8;;")
		output.WriteString(link)
		output.WriteString("\x1b\\")
	}
	*current = link
}

func writeTextForeground(output *strings.Builder, foreground terminalColor, current *terminalColor) {
	if foreground == *current {
		return
	}
	output.WriteString(ansiForeground(foreground))
	*current = foreground
}

func writeTextAttributes(output *strings.Builder, bold, italic bool, currentBold, currentItalic *bool) {
	if bold != *currentBold {
		if bold {
			output.WriteString(ansiBold)
		} else {
			output.WriteString(ansiNormalIntensity)
		}
		*currentBold = bold
	}
	if italic != *currentItalic {
		if italic {
			output.WriteString(ansiItalic)
		} else {
			output.WriteString(ansiNotItalic)
		}
		*currentItalic = italic
	}
}

func writeCursorPosition(output *strings.Builder, row, column int) {
	output.WriteString("\x1b[")
	output.WriteString(strconv.Itoa(row))
	output.WriteByte(';')
	output.WriteString(strconv.Itoa(column))
	output.WriteByte('H')
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\t", "    ")
	paragraphs := strings.Split(text, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}

		var line strings.Builder
		lineWidth := 0
		for _, character := range paragraph {
			characterWidth := runeWidth(character)
			if lineWidth > 0 && lineWidth+characterWidth > width {
				lines = append(lines, line.String())
				line.Reset()
				lineWidth = 0
			}
			line.WriteRune(character)
			lineWidth += characterWidth
		}
		lines = append(lines, line.String())
	}
	return lines
}

func truncateCells(value string, width int, ellipsis bool) string {
	if width <= 0 {
		return ""
	}
	if cellWidth(value) <= width {
		return value
	}

	limit := width
	suffix := ""
	if ellipsis && width > 1 {
		limit--
		suffix = "…"
	}
	var result strings.Builder
	used := 0
	for _, character := range value {
		characterWidth := runeWidth(character)
		if used+characterWidth > limit {
			break
		}
		result.WriteRune(character)
		used += characterWidth
	}
	result.WriteString(suffix)
	return result.String()
}

func cellWidth(value string) int {
	return runesWidth([]rune(value))
}

func runesWidth(value []rune) int {
	width := 0
	for _, character := range value {
		width += runeWidth(character)
	}
	return width
}

func runeWidth(character rune) int {
	if character == 0 || unicode.Is(unicode.Mn, character) || unicode.Is(unicode.Me, character) || unicode.Is(unicode.Cf, character) {
		return 0
	}
	if unicode.IsControl(character) {
		return 0
	}
	if isWideRune(character) {
		return 2
	}
	return 1
}

func isWideRune(character rune) bool {
	return character >= 0x1100 && (character <= 0x115f ||
		character == 0x2329 || character == 0x232a ||
		character >= 0x2e80 && character <= 0xa4cf && character != 0x303f ||
		character >= 0xac00 && character <= 0xd7a3 ||
		character >= 0xf900 && character <= 0xfaff ||
		character >= 0xfe10 && character <= 0xfe19 ||
		character >= 0xfe30 && character <= 0xfe6f ||
		character >= 0xff00 && character <= 0xff60 ||
		character >= 0xffe0 && character <= 0xffe6 ||
		character >= 0x1f300 && character <= 0x1faff ||
		character >= 0x20000 && character <= 0x3fffd)
}
