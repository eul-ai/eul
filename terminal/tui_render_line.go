package terminal

import (
	"strconv"
	"strings"

	"github.com/eul-ai/eul/terminal/text"
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
	rightForeground  *terminalColor
	spans            []text.Span
	style            lineStyle
	padding          int
	breakBefore      text.Break
}

type fittedLine struct {
	leftPadding  int
	rightPadding int
	textWidth    int
	prefix       string
	text         string
	right        string
	spans        []text.Span
}

func fitLine(line styledLine, width int) fittedLine {
	leftPadding := min(line.padding, width)
	rightPadding := min(line.padding, width-leftPadding)
	contentWidth := width - leftPadding - rightPadding
	right := text.Truncate(line.rightText, contentWidth, false)
	textWidth := contentWidth - text.Width(right)
	prefix := text.Truncate(line.prefixText, textWidth, false)
	remainingTextWidth := textWidth - text.Width(prefix)
	value := text.Truncate(line.text, remainingTextWidth, false)
	spans := text.TruncateSpans(line.spans, remainingTextWidth)
	if len(spans) > 0 {
		value = text.Plain(spans)
	}
	return fittedLine{
		leftPadding:  leftPadding,
		rightPadding: rightPadding,
		textWidth:    textWidth,
		prefix:       prefix,
		text:         value,
		right:        right,
		spans:        spans,
	}
}

func renderedLineText(line styledLine, width int) string {
	fitted := fitLine(line, width)
	return strings.Repeat(" ", fitted.leftPadding) + fitted.prefix + fitted.text +
		strings.Repeat(" ", fitted.textWidth-text.Width(fitted.prefix)-text.Width(fitted.text)) +
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
			switch span.Style.Foreground {
			case text.ForegroundAccent:
				spanForeground = currentTheme.accent
			case text.ForegroundMuted:
				spanForeground = currentTheme.muted
			case text.ForegroundOrange:
				spanForeground = currentTheme.orange
			case text.ForegroundSuccess:
				spanForeground = currentTheme.green
			case text.ForegroundError:
				spanForeground = currentTheme.error
			}
			if span.Style.Code {
				spanForeground = currentTheme.markdownCode
			}
			writeTextForeground(frame, spanForeground, &foreground)
			writeTextAttributes(frame, style.bold || span.Style.Bold, style.italic || span.Style.Italic, &bold, &italic)
			writeLink(frame, span.Style.Link, &link)
			frame.WriteString(span.Text)
		}
		writeLink(frame, "", &link)
		writeTextForeground(frame, style.foreground, &foreground)
		writeTextAttributes(frame, style.bold, style.italic, &bold, &italic)
	}
	frame.WriteString(strings.Repeat(" ", fitted.textWidth-text.Width(fitted.prefix)-text.Width(fitted.text)))
	if line.rightForeground != nil {
		writeTextForeground(frame, *line.rightForeground, &foreground)
	}
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
