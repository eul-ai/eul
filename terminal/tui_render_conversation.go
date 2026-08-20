package terminal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eul-ai/eul/agent"
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

func conversationLines(blocks []conversationBlock, width int) []styledLine {
	var lines []styledLine
	for index, block := range blocks {
		lines = append(lines, conversationBlockLines(block, width)...)
		if index < len(blocks)-1 {
			lines = append(lines, styledLine{})
		}
	}
	return lines
}

func wrapHardConversationText(text string, width int) []formattedLine {
	wrapped := wrapText(text, width)
	lines := make([]formattedLine, len(wrapped))
	line := 0
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n") {
		count := len(wrapText(paragraph, width))
		for offset := range count {
			lines[line+offset] = formattedLine{
				text: wrapped[line+offset], breakBefore: lineBreak{continuation: offset > 0},
			}
		}
		line += count
	}
	return lines
}

func wrapConversationProse(text string, width int) []formattedLine {
	lines := wrapInlineSpans([]inlineSpan{{text: text}}, width)
	for index := range lines {
		lines[index].spans = nil
	}
	return lines
}

func contentDisplaySpans(content []agent.ContentPart) []inlineSpan {
	var spans []inlineSpan
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			for _, span := range parseInlineMarkdown(part.Text) {
				appendInlineSpan(&spans, span.text, span.style)
			}
		case agent.ContentPartImage:
			spans = append(spans, inlineSpan{text: imageAttachmentLabel, atomic: true})
		}
	}
	return spans
}

func displayContent(content []agent.ContentPart) string {
	return inlineSpanText(contentDisplaySpans(content))
}

func conversationBlockLines(block conversationBlock, width int) []styledLine {
	lines, _ := renderConversationBlockLines(block, width)
	return lines
}

func renderConversationBlockLines(block conversationBlock, width int) ([]styledLine, bool) {
	style := blockPresentation(block.kind)
	text := block.text
	if block.kind == blockReasoning {
		text = strings.Trim(text, "\n")
	}
	if block.kind == blockUser && len(block.content) > 0 {
		text = displayContent(block.content)
	}

	padding := conversationPadding
	contentWidth := width - padding*2
	if contentWidth < 1 {
		contentWidth = 1
	}

	var lines []styledLine
	collapsible := false
	switch {
	case isToolBlock(block.kind):
		lines = append(lines, styledLine{style: style, padding: padding})
		toolLines, toolCollapsible := toolConversationLines(block, contentWidth, style, padding)
		lines = append(lines, toolLines...)
		lines = append(lines, styledLine{style: style, padding: padding})
		collapsible = toolCollapsible
	case block.kind == blockUser && len(block.content) > 0 && contentHasImage(block.content):
		for _, line := range wrapInlineSpans(contentDisplaySpans(block.content), contentWidth) {
			lines = append(lines, styledLine{
				text: line.text, spans: line.spans, style: style, padding: padding,
				breakBefore: line.breakBefore,
			})
		}
	case block.kind == blockUser && len(block.content) > 0:
		lines = append(lines, markdownConversationLines(contentText(block.content), contentWidth, style, padding)...)
	case isInlineMarkdownBlock(block.kind):
		lines = append(lines, markdownConversationLines(text, contentWidth, style, padding)...)
	default:
		for _, line := range wrapConversationProse(text, contentWidth) {
			lines = append(lines, styledLine{
				text: line.text, style: style, padding: padding,
				breakBefore: line.breakBefore,
			})
		}
	}
	return lines, collapsible
}

func markdownConversationLines(text string, width int, style lineStyle, padding int) []styledLine {
	var lines []styledLine
	for _, line := range wrapMarkdown(text, width) {
		lineStyle := style
		styled := styledLine{
			text: line.text, spans: line.spans, style: lineStyle, padding: padding,
			breakBefore: line.breakBefore,
		}
		switch {
		case line.fencedCode:
			styled.style.foreground = currentTheme.markdownCode
			styled.style.bold = false
			styled.style.italic = false
		case line.thematicBreak:
			styled.style.foreground = currentTheme.muted
			styled.style.bold = false
			styled.style.italic = false
		}
		lines = append(lines, styled)
	}
	return lines
}

func toolConversationLines(block conversationBlock, width int, style lineStyle, padding int) ([]styledLine, bool) {
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
		lines = append(lines, styledLine{
			text: line.text, spans: line.spans, style: style, padding: padding,
			breakBefore: line.breakBefore,
		})
	}
	if len(block.tool.Lines) == 0 && len(block.tool.Diff) == 0 && block.tool.Elapsed == 0 {
		return lines, false
	}
	lines = append(lines, styledLine{style: style, padding: padding})

	contentAdded := false
	collapsible := false
	if len(block.tool.Lines) > 0 {
		var bodyLines []styledLine
		body := strings.Join(block.tool.Lines, "\n")
		if block.tool.Markdown {
			bodyLines = append(bodyLines, markdownConversationLines(body, width, style, padding)...)
		} else {
			for _, line := range wrapHardConversationText(body, width) {
				bodyLines = append(bodyLines, styledLine{
					text: line.text, style: style, padding: padding,
					breakBefore: line.breakBefore,
				})
			}
		}
		if block.tool.LinesTruncated {
			markerLines := len(wrapText(block.tool.Lines[len(block.tool.Lines)-1], width))
			for index := max(0, len(bodyLines)-markerLines); index < len(bodyLines); index++ {
				bodyLines[index].style.foreground = currentTheme.muted
			}
		}
		limit, fromTail := toolCollapseLimit(block.tool)
		if limit > 0 && len(bodyLines) > limit {
			collapsible = true
			if !block.expanded {
				omitted := len(bodyLines) - limit
				omittedStyle := style
				omittedStyle.foreground = currentTheme.muted
				marker := styledLine{text: toolOmissionMarker(omitted, width, fromTail), style: omittedStyle, padding: padding}
				if fromTail {
					lines = append(lines, marker)
					bodyLines = bodyLines[omitted:]
				} else {
					bodyLines = append(bodyLines[:limit], marker)
				}
			}
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
		elapsed := fmt.Sprintf("%s %.1fs", label, block.tool.Elapsed.Seconds())
		if block.tool.Timeout > 0 {
			timeout := strconv.FormatFloat(block.tool.Timeout.Seconds(), 'f', -1, 64)
			elapsed += fmt.Sprintf(" (%ss timeout)", timeout)
		}
		lines = append(lines, styledLine{text: elapsed, style: elapsedStyle, padding: padding})
	}
	return lines, collapsible
}

func toolCollapseLimit(presentation agent.ToolPresentation) (int, bool) {
	if presentation.TailLines > 0 {
		return presentation.TailLines, true
	}
	return presentation.HeadLines, false
}

func toolOmissionMarker(omitted, width int, earlier bool) string {
	direction := "more"
	if earlier {
		direction = "earlier"
	}
	marker := fmt.Sprintf("... (%d %s lines)", omitted, direction)
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

func isInlineMarkdownBlock(kind blockKind) bool {
	switch kind {
	case blockUser, blockAssistant, blockReasoning:
		return true
	default:
		return false
	}
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

func toggleVisibleOutput(model *tuiModel, frame terminalFrame) {
	top := frame.conversationTop
	bottom := top + frame.layout.conversationHeight
	visible := make([]conversationBlockProjection, 0, len(frame.conversationBlocks))
	showAll := false
	for _, projection := range frame.conversationBlocks {
		if !projection.collapsible || projection.start >= bottom || projection.end <= top {
			continue
		}
		visible = append(visible, projection)
		showAll = showAll || !projection.expanded
	}
	if len(visible) == 0 {
		return
	}

	nextTop := top
	for _, projection := range visible {
		if projection.index < 0 || projection.index >= len(model.blocks) {
			continue
		}
		block := &model.blocks[projection.index]
		if block.expanded == showAll {
			continue
		}

		block.expanded = showAll
		if !model.following && projection.start < top && projection.end > top {
			rendered := renderConversationBlock(*block, frame.width)
			oldLines := frame.conversationLines[projection.start:projection.end]
			nextTop = toggledBlockScrollTop(top, projection.start, oldLines, rendered.plain)
		}
	}

	model.scrollTop = max(0, nextTop)
	model.selection = textSelection{}
	model.conversationChanged()
}

func toggledBlockScrollTop(top, blockStart int, oldLines, newLines []string) int {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	relativeTop := top - blockStart
	switch {
	case relativeTop < prefix:
		return top
	case relativeTop >= len(oldLines)-suffix:
		return top + len(newLines) - len(oldLines)
	default:
		return blockStart + prefix
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
		model.scrollTop = 0
	}
	if model.scrollTop >= bottom {
		model.scrollTop = bottom
		model.following = true
	}
}
