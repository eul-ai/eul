package terminal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal/text"
)

const (
	conversationPadding         = 1
	conversationVerticalPadding = 1
)

func conversationViewport(lines []styledLine, scrollTop, height int) []styledLine {
	if height <= 0 {
		return nil
	}
	visible := make([]styledLine, height)
	end := min(len(lines), scrollTop+height)
	if scrollTop < end {
		copy(visible, lines[scrollTop:end])
	}
	return visible
}

func styledLines(lines []text.Line, style lineStyle, padding int) []styledLine {
	styled := make([]styledLine, len(lines))
	for index, line := range lines {
		styled[index] = styledLine{text: line.Text(), spans: line.Spans, style: style, padding: padding, breakBefore: line.Break}
	}
	return styled
}

func hardWrappedLines(value string, width int, style lineStyle, padding int) []styledLine {
	var lines []styledLine
	for _, paragraph := range strings.Split(value, "\n") {
		for index, wrapped := range text.Wrap(paragraph, width) {
			lines = append(lines, styledLine{text: wrapped, style: style, padding: padding, breakBefore: text.Break{Continuation: index > 0}})
		}
	}
	return lines
}

func contentDisplaySpans(content []agent.ContentPart) []text.Span {
	var spans []text.Span
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			for _, span := range text.ParseInline(part.Text) {
				spans = text.AppendSpan(spans, span.Text, span.Style)
			}
		case agent.ContentPartImage:
			spans = append(spans, text.Span{Text: imageAttachmentLabel, Atomic: true})
		}
	}
	return spans
}

func displayContent(content []agent.ContentPart) string {
	return text.Plain(contentDisplaySpans(content))
}

func conversationContentWidth(width int) int {
	return max(1, width-conversationPadding*2)
}

func conversationBlockLines(block session.Entry, width int) []styledLine {
	style := blockPresentation(block.Kind)
	padding := conversationPadding
	contentWidth := conversationContentWidth(width)
	markdown, isMarkdown := markdownSource(block)

	var lines []styledLine
	switch {
	case isToolBlock(block.Kind):
		lines = append(lines, styledLine{style: style, padding: padding})
		lines = append(lines, toolConversationLines(block, contentWidth, style, padding)...)
		lines = append(lines, styledLine{style: style, padding: padding})
	case block.Kind == session.EntryUser && contentHasImage(block.Content):
		lines = append(lines, styledLines(text.WrapSpans(contentDisplaySpans(block.Content), contentWidth), style, padding)...)
	case isMarkdown:
		lines = append(lines, markdownConversationLines(markdown, contentWidth, style, padding)...)
	default:
		for _, line := range text.WrapSpans([]text.Span{{Text: block.Text}}, contentWidth) {
			lines = append(lines, styledLine{text: line.Text(), style: style, padding: padding, breakBefore: line.Break})
		}
	}
	return lines
}

func markdownConversationLines(source string, width int, style lineStyle, padding int) []styledLine {
	lines := text.WrapMarkdown(source, width)
	styled := styledLines(lines, style, padding)
	for index, line := range lines {
		switch {
		case line.FencedCode:
			styled[index].style.foreground = currentTheme.markdownCode
		case line.ThematicBreak:
			styled[index].style.foreground = currentTheme.muted
		default:
			continue
		}
		styled[index].style.bold = false
		styled[index].style.italic = false
	}
	return styled
}

func toolConversationLines(block session.Entry, width int, style lineStyle, padding int) []styledLine {
	titleForeground := text.ForegroundAccent
	if block.Kind == session.EntryToolError {
		titleForeground = text.ForegroundError
	}
	heading := []text.Span{{Text: block.ToolName, Style: text.Style{Bold: true, Foreground: titleForeground}}}
	if block.Tool.Arguments != "" {
		heading = text.AppendSpan(heading, " "+block.Tool.Arguments, text.Style{})
	}
	if block.Tool.Outcome != "" {
		heading = text.AppendSpan(heading, " — "+block.Tool.Outcome, text.Style{Foreground: titleForeground})
	}

	lines := make([]styledLine, 0, len(block.Tool.Lines)+len(block.Tool.Diff)+4)
	lines = append(lines, styledLines(text.WrapSpans(heading, width), style, padding)...)
	if len(block.Tool.Lines) == 0 && len(block.Tool.Diff) == 0 && block.Tool.Elapsed == 0 {
		return lines
	}
	lines = append(lines, styledLine{style: style, padding: padding})

	contentAdded := false
	if len(block.Tool.Lines) > 0 {
		var bodyLines []styledLine
		body := sanitizeAssistantText(strings.Join(block.Tool.Lines, "\n"))
		if block.Tool.Markdown {
			bodyLines = append(bodyLines, markdownConversationLines(body, width, style, padding)...)
		} else {
			bodyLines = append(bodyLines, hardWrappedLines(body, width, style, padding)...)
		}
		if block.Tool.LinesTruncated {
			markerLines := len(text.Wrap(sanitizeAssistantText(block.Tool.Lines[len(block.Tool.Lines)-1]), width))
			for index := max(0, len(bodyLines)-markerLines); index < len(bodyLines); index++ {
				bodyLines[index].style.foreground = currentTheme.muted
			}
		}
		if limit := block.Tool.TailLines; limit > 0 && len(bodyLines) > limit {
			omitted := len(bodyLines) - limit
			omittedStyle := style
			omittedStyle.foreground = currentTheme.muted
			lines = append(lines, styledLine{text: toolOmissionMarker(omitted, width), style: omittedStyle, padding: padding})
			bodyLines = bodyLines[omitted:]
		}
		lines = append(lines, bodyLines...)
		contentAdded = true
	}
	if len(block.Tool.Diff) > 0 {
		lines = append(lines, toolDiffConversationLines(block.Tool.Diff, width, style, padding)...)
		contentAdded = true
	}
	if block.Tool.Elapsed > 0 {
		if contentAdded {
			lines = append(lines, styledLine{style: style, padding: padding})
		}
		elapsedStyle := style
		elapsedStyle.foreground = currentTheme.muted
		label := "Took"
		if block.Kind == session.EntryToolPending {
			label = "Elapsed"
		}
		elapsed := fmt.Sprintf("%s %.1fs", label, block.Tool.Elapsed.Seconds())
		if block.Tool.Timeout > 0 {
			timeout := strconv.FormatFloat(block.Tool.Timeout.Seconds(), 'f', -1, 64)
			elapsed += fmt.Sprintf(" (%ss timeout)", timeout)
		}
		lines = append(lines, styledLine{text: elapsed, style: elapsedStyle, padding: padding})
	}
	return lines
}

func toolOmissionMarker(omitted, width int) string {
	marker := fmt.Sprintf("... (%d earlier lines)", omitted)
	if text.Width(marker) <= width {
		return marker
	}
	return text.Truncate(fmt.Sprintf("... (+%d)", omitted), width, false)
}

func toolDiffConversationLines(diff []agent.ToolDiffLine, width int, style lineStyle, padding int) []styledLine {
	lineNumberWidth := 1
	for _, line := range diff {
		lineNumberWidth = max(lineNumberWidth, len(strconv.Itoa(max(line.OldLine, line.NewLine))))
	}

	lines := make([]styledLine, 0, len(diff))
	for _, line := range diff {
		prefix := " "
		lineNumber := line.OldLine
		lineStyle := style
		lineStyle.foreground = currentTheme.diffContext
		switch line.Kind {
		case agent.ToolDiffLineAdded:
			prefix = "+"
			lineNumber = line.NewLine
			lineStyle.foreground = currentTheme.diffAdded
		case agent.ToolDiffLineRemoved:
			prefix = "-"
			lineStyle.foreground = currentTheme.diffRemoved
		}

		lineNumberText := strings.Repeat(" ", lineNumberWidth)
		if lineNumber > 0 {
			lineNumberText = fmt.Sprintf("%*d", lineNumberWidth, lineNumber)
		}
		for _, wrapped := range text.Wrap(prefix+lineNumberText+" "+sanitizeAssistantText(line.Text), width) {
			lines = append(lines, styledLine{text: wrapped, style: lineStyle, padding: padding})
		}
	}
	return lines
}

func isToolBlock(kind session.EntryKind) bool {
	return kind == session.EntryToolPending || kind == session.EntryTool || kind == session.EntryToolError
}

func markdownSource(block session.Entry) (string, bool) {
	switch block.Kind {
	case session.EntryUser:
		switch {
		case len(block.Content) == 0:
			return block.Text, true
		case contentHasImage(block.Content):
			return "", false
		default:
			return contentText(block.Content), true
		}
	case session.EntryAssistant:
		return block.Text, true
	case session.EntryReasoning:
		return strings.Trim(block.Text, "\n"), true
	default:
		return "", false
	}
}

func blockPresentation(kind session.EntryKind) lineStyle {
	switch kind {
	case session.EntryUser:
		return lineStyle{foreground: currentTheme.yellow}
	case session.EntryAssistant:
		return lineStyle{foreground: currentTheme.foreground}
	case session.EntryReasoning:
		return lineStyle{foreground: currentTheme.muted, italic: true}
	case session.EntryContext:
		return lineStyle{foreground: currentTheme.muted}
	case session.EntryToolPending:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolPendingBackground, paintBackground: true}
	case session.EntryTool:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolSuccessBackground, paintBackground: true}
	case session.EntryToolError:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolErrorBackground, paintBackground: true}
	case session.EntryError:
		return lineStyle{foreground: currentTheme.error}
	case session.EntryInfo, session.EntryNotice:
		return lineStyle{foreground: currentTheme.muted}
	default:
		return lineStyle{foreground: currentTheme.foreground}
	}
}

func scrollConversation(model *tuiModel, direction int, frame terminalFrame) {
	scrollConversationBy(model, direction*frame.layout.conversationHeight, frame)
}

func scrollConversationToBottom(model *tuiModel, frame terminalFrame) {
	height := frame.layout.conversationHeight
	if height <= 0 {
		return
	}

	model.scrollTop = max(0, len(frame.conversationLines)-height)
	model.following = true
}

func scrollConversationBy(model *tuiModel, lines int, frame terminalFrame) {
	height := frame.layout.conversationHeight
	if height <= 0 || lines == 0 {
		return
	}

	bottom := max(0, len(frame.conversationLines)-height)
	if model.following {
		model.scrollTop = bottom
	}
	model.following = false
	model.scrollTop += lines
	if model.scrollTop <= 0 {
		if frame.conversationTruncated {
			model.historyExpansionRequested = true
		} else {
			model.scrollTop = 0
		}
	}
	if model.scrollTop >= bottom {
		model.scrollTop = bottom
		model.following = true
	}
}
