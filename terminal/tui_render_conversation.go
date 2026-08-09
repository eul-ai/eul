package terminal

import (
	"fmt"
	"strconv"
	"strings"

	"yaah/agent"
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

func modelConversationLines(model *tuiModel, width int) []styledLine {
	blocks := append([]conversationBlock(nil), model.blocks...)
	for _, message := range model.steering {
		blocks = append(blocks, conversationBlock{kind: blockInfo, text: "Queued: " + message})
	}
	lines := conversationLines(blocks, width)
	result := make([]styledLine, 0, len(lines)+conversationVerticalPadding*2)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	result = append(result, lines...)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	return result
}

func conversationLines(blocks []conversationBlock, width int) []styledLine {
	var lines []styledLine
	for index, block := range blocks {
		style := blockPresentation(block.kind)
		text := block.text
		if block.kind == blockReasoning {
			text = strings.Trim(text, "\n")
		}

		padding := conversationPadding
		contentWidth := width - padding*2
		if contentWidth < 1 {
			contentWidth = 1
		}
		if isToolBlock(block.kind) {
			lines = append(lines, styledLine{style: style, padding: padding})
			lines = append(lines, toolConversationLines(block, contentWidth, style, padding)...)
			lines = append(lines, styledLine{style: style, padding: padding})
		} else if block.kind == blockAssistant || block.kind == blockReasoning {
			for _, line := range wrapInlineMarkdown(text, contentWidth) {
				lines = append(lines, styledLine{text: line.text, spans: line.spans, style: style, padding: padding})
			}
		} else {
			for _, line := range wrapText(text, contentWidth) {
				lines = append(lines, styledLine{text: line, style: style, padding: padding})
			}
		}
		if index < len(blocks)-1 {
			lines = append(lines, styledLine{})
		}
	}
	return lines
}

func toolConversationLines(block conversationBlock, width int, style lineStyle, padding int) []styledLine {
	title := block.tool.Title
	if title == "" {
		title = block.text
	}
	titleForeground := inlineForegroundAccent
	if block.kind == blockToolError {
		titleForeground = inlineForegroundError
	}
	heading := []inlineSpan{{text: title, style: inlineStyle{bold: true, foreground: titleForeground}}}
	if block.tool.Arguments != "" {
		appendInlineSpan(&heading, " "+block.tool.Arguments, inlineStyle{})
	}
	outcome := block.toolOutcome
	if outcome == "" && block.kind == blockToolPending {
		outcome = block.tool.Outcome
	}
	if outcome != "" {
		appendInlineSpan(&heading, " — "+outcome, inlineStyle{foreground: titleForeground})
	}

	lines := make([]styledLine, 0, len(block.tool.Lines)+len(block.tool.Diff)+4)
	for _, line := range wrapInlineSpans(heading, width) {
		lines = append(lines, styledLine{text: line.text, spans: line.spans, style: style, padding: padding})
	}
	if len(block.tool.Lines) == 0 && len(block.tool.Diff) == 0 && block.tool.Elapsed == 0 {
		return lines
	}
	lines = append(lines, styledLine{style: style, padding: padding})

	contentAdded := false
	if len(block.tool.Lines) > 0 {
		var bodyLines []styledLine
		body := strings.Join(block.tool.Lines, "\n")
		if block.tool.Markdown {
			for _, line := range wrapInlineMarkdown(body, width) {
				bodyLines = append(bodyLines, styledLine{text: line.text, spans: line.spans, style: style, padding: padding})
			}
		} else {
			for _, line := range wrapText(body, width) {
				bodyLines = append(bodyLines, styledLine{text: line, style: style, padding: padding})
			}
		}
		if limit := block.tool.TailLines; limit > 0 && len(bodyLines) > limit {
			omitted := len(bodyLines) - limit
			omittedStyle := style
			omittedStyle.foreground = currentTheme.muted
			lines = append(lines, styledLine{text: toolOmissionMarker(omitted, width), style: omittedStyle, padding: padding})
			bodyLines = bodyLines[omitted:]
		}
		lines = append(lines, bodyLines...)
		contentAdded = true
	}
	if len(block.tool.Diff) > 0 {
		lines = append(lines, toolDiffConversationLines(block.tool.Diff, width, style, padding)...)
		contentAdded = true
	}
	if block.tool.Elapsed > 0 {
		if contentAdded {
			lines = append(lines, styledLine{style: style, padding: padding})
		}
		elapsedStyle := style
		elapsedStyle.foreground = currentTheme.muted
		label := "Took"
		if block.kind == blockToolPending {
			label = "Elapsed"
		}
		lines = append(lines, styledLine{text: fmt.Sprintf("%s %.1fs", label, block.tool.Elapsed.Seconds()), style: elapsedStyle, padding: padding})
	}
	return lines
}

func toolOmissionMarker(omitted, width int) string {
	marker := fmt.Sprintf("... (%d earlier lines)", omitted)
	if cellWidth(marker) <= width {
		return marker
	}
	return truncateCells(fmt.Sprintf("... (+%d)", omitted), width, false)
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
		text := prefix + lineNumberText + " " + line.Text
		for _, wrapped := range wrapText(text, width) {
			lines = append(lines, styledLine{text: wrapped, style: lineStyle, padding: padding})
		}
	}
	return lines
}

func isToolBlock(kind blockKind) bool {
	return kind == blockToolPending || kind == blockTool || kind == blockToolError
}

func blockPresentation(kind blockKind) lineStyle {
	switch kind {
	case blockUser:
		return lineStyle{foreground: currentTheme.yellow}
	case blockAssistant:
		return lineStyle{foreground: currentTheme.foreground}
	case blockReasoning:
		return lineStyle{foreground: currentTheme.muted, italic: true}
	case blockContext:
		return lineStyle{foreground: currentTheme.muted}
	case blockToolPending:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolPendingBackground, paintBackground: true}
	case blockTool:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolSuccessBackground, paintBackground: true}
	case blockToolError:
		return lineStyle{foreground: currentTheme.foreground, background: currentTheme.toolErrorBackground, paintBackground: true}
	case blockError:
		return lineStyle{foreground: currentTheme.error}
	case blockInfo:
		return lineStyle{foreground: currentTheme.muted}
	default:
		return lineStyle{foreground: currentTheme.foreground}
	}
}

func scrollConversation(model *tuiModel, direction int, frame terminalFrame) {
	scrollConversationBy(model, direction*frame.layout.conversationHeight, frame)
}

func scrollConversationBy(model *tuiModel, lines int, frame terminalFrame) {
	height := frame.layout.conversationHeight
	if height <= 0 || lines == 0 {
		return
	}

	bottom := max(0, len(frame.conversationLines)-height)
	if model.following {
		model.scrollTop = frame.conversationTop
	}
	model.following = false
	model.scrollTop += lines
	if model.scrollTop <= 0 {
		model.scrollTop = 0
	}
	if model.scrollTop >= bottom {
		model.scrollTop = bottom
		model.following = true
	}
}
