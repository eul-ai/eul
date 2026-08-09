package terminal

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"yaah/agent"
)

const (
	ansiHideCursor              = "\x1b[?25l"
	ansiShowCursor              = "\x1b[?25h"
	ansiReset                   = "\x1b[0m"
	ansiBold                    = "\x1b[1m"
	ansiNormalIntensity         = "\x1b[22m"
	ansiItalic                  = "\x1b[3m"
	ansiNotItalic               = "\x1b[23m"
	ansiReverse                 = "\x1b[7m"
	ansiNotReverse              = "\x1b[27m"
	ansiBeginSynchronizedOutput = "\x1b[?2026h"
	ansiEndSynchronizedOutput   = "\x1b[?2026l"
	ansiClearScreen             = "\x1b[2J"
	ansiScrollUp                = "\x1b[1S"
	ansiScrollDown              = "\x1b[1T"
	ansiResetScrollRegion       = "\x1b[r"
	conversationPadding         = 1
	conversationVerticalPadding = 1
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

type tuiLayout struct {
	conversationHeight int
	topRuleRow         int
	inputRow           int
	inputHeight        int
	bottomRuleRow      int
	pickerRow          int
	pickerHeight       int
	statusRow          int
}

type renderedInput struct {
	lines        []string
	cursorRow    int
	cursorColumn int
}

type terminalFrame struct {
	width               int
	height              int
	rows                []string
	plainRows           []string
	cursorRow           int
	cursorColumn        int
	cursorVisible       bool
	layout              tuiLayout
	conversationTop     int
	conversationLines   []string
	conversationVersion uint64
}

type renderPreparation struct {
	input             renderedInput
	layout            tuiLayout
	conversationLines []styledLine
	conversationPlain []string
	scrollTop         int
}

type tuiRenderer struct {
	frame               terminalFrame
	conversationLines   []styledLine
	conversationPlain   []string
	conversationWidth   int
	conversationVersion uint64
}

func calculateLayout(height, inputHeight, pickerHeight int) tuiLayout {
	if height <= 0 {
		return tuiLayout{}
	}
	if height == 1 {
		return tuiLayout{statusRow: 1}
	}
	if height < 4 {
		return tuiLayout{
			conversationHeight: height - 2,
			inputRow:           height - 1,
			inputHeight:        1,
			statusRow:          height,
		}
	}

	pickerHeight = max(0, min(pickerHeight, height-4))
	inputHeight = max(1, min(inputHeight, height-pickerHeight-3))
	conversationHeight := height - inputHeight - pickerHeight - 3
	bottomRuleRow := conversationHeight + inputHeight + 2
	pickerRow := 0
	if pickerHeight > 0 {
		pickerRow = bottomRuleRow + 1
	}
	return tuiLayout{
		conversationHeight: conversationHeight,
		topRuleRow:         conversationHeight + 1,
		inputRow:           conversationHeight + 2,
		inputHeight:        inputHeight,
		bottomRuleRow:      bottomRuleRow,
		pickerRow:          pickerRow,
		pickerHeight:       pickerHeight,
		statusRow:          height,
	}
}

func maximumInputHeight(height, pickerHeight int) int {
	switch {
	case height <= 1:
		return 0
	case height < 5:
		return 1
	default:
		return max(1, height-pickerHeight-4)
	}
}

func maximumFilePickerHeight(height int) int {
	return max(0, height-5)
}

func modelInputLayout(model *tuiModel) (renderedInput, tuiLayout) {
	pickerHeight := min(model.filePickerHeight(), maximumFilePickerHeight(model.height))
	input := renderInput(model, model.width, maximumInputHeight(model.height, pickerHeight))
	return input, calculateLayout(model.height, len(input.lines), pickerHeight)
}

func (r *tuiRenderer) render(model *tuiModel) string {
	r.normalizeViewport(model)
	output, next := r.renderPending(model, false)
	r.commit(next)
	return output
}

func (r *tuiRenderer) renderPending(model *tuiModel, forceRedraw bool) (string, terminalFrame) {
	prepared := r.prepare(model)
	next := projectTerminalFrame(model, prepared)
	if next.width < 1 || next.height < 1 {
		return "", next
	}

	previous := r.frame
	resized := previous.width != 0 && (previous.width != next.width || previous.height != next.height)
	full := previous.width == 0 || resized || forceRedraw || len(previous.rows) != len(next.rows)
	scroll, scrolling := conversationScrollUpdate(previous, next, full)
	changed := make([]int, 0, len(next.rows))
	for index, row := range next.rows {
		if scrolling && index < next.layout.conversationHeight {
			if index == scroll.exposedRow {
				changed = append(changed, index)
			}
			continue
		}
		if full || row != previous.rows[index] {
			changed = append(changed, index)
		}
	}
	cursorChanged := previous.cursorRow != next.cursorRow || previous.cursorColumn != next.cursorColumn || previous.cursorVisible != next.cursorVisible
	if len(changed) == 0 && !cursorChanged {
		return "", next
	}

	var output strings.Builder
	output.WriteString(ansiBeginSynchronizedOutput)
	output.WriteString(ansiHideCursor)
	if resized || forceRedraw {
		output.WriteString(ansiClearScreen)
	}
	if scrolling {
		writeConversationScroll(&output, next.layout.conversationHeight, scroll.delta)
	}
	for _, index := range changed {
		output.WriteString(next.rows[index])
	}
	if next.cursorVisible {
		writeCursorPosition(&output, next.cursorRow, next.cursorColumn)
		output.WriteString(ansiShowCursor)
	}
	output.WriteString(ansiEndSynchronizedOutput)
	return output.String(), next
}

type conversationScroll struct {
	delta      int
	exposedRow int
}

func conversationScrollUpdate(previous, next terminalFrame, full bool) (conversationScroll, bool) {
	height := next.layout.conversationHeight
	if full || height < 2 || previous.layout != next.layout || previous.conversationVersion != next.conversationVersion {
		return conversationScroll{}, false
	}

	delta := next.conversationTop - previous.conversationTop
	switch delta {
	case 1:
		for row := 0; row < height-1; row++ {
			if !sameRenderedRow(next.rows[row], row+1, previous.rows[row+1], row+2) {
				return conversationScroll{}, false
			}
		}
		return conversationScroll{delta: delta, exposedRow: height - 1}, true
	case -1:
		for row := 1; row < height; row++ {
			if !sameRenderedRow(next.rows[row], row+1, previous.rows[row-1], row) {
				return conversationScroll{}, false
			}
		}
		return conversationScroll{delta: delta}, true
	default:
		return conversationScroll{}, false
	}
}

func sameRenderedRow(left string, leftRow int, right string, rightRow int) bool {
	left, leftOK := strings.CutPrefix(left, "\x1b["+strconv.Itoa(leftRow)+";1H")
	right, rightOK := strings.CutPrefix(right, "\x1b["+strconv.Itoa(rightRow)+";1H")
	return leftOK && rightOK && left == right
}

func writeConversationScroll(output *strings.Builder, height, delta int) {
	output.WriteString("\x1b[1;")
	output.WriteString(strconv.Itoa(height))
	output.WriteByte('r')
	if delta > 0 {
		output.WriteString(ansiScrollUp)
	} else {
		output.WriteString(ansiScrollDown)
	}
	output.WriteString(ansiResetScrollRegion)
}

func (r *tuiRenderer) prepare(model *tuiModel) renderPreparation {
	input, layout := modelInputLayout(model)
	if r.conversationWidth != model.width || r.conversationVersion != model.conversationVersion {
		r.conversationLines = modelConversationLines(model, model.width)
		r.conversationPlain = make([]string, len(r.conversationLines))
		for index, line := range r.conversationLines {
			r.conversationPlain[index] = renderedLineText(line, model.width)
		}
		r.conversationWidth = model.width
		r.conversationVersion = model.conversationVersion
	}

	return renderPreparation{
		input:             input,
		layout:            layout,
		conversationLines: r.conversationLines,
		conversationPlain: r.conversationPlain,
		scrollTop:         model.scrollTop,
	}
}

func (r *tuiRenderer) normalizeViewport(model *tuiModel) {
	prepared := r.prepare(model)
	bottom := max(0, len(prepared.conversationLines)-prepared.layout.conversationHeight)
	if model.following {
		model.scrollTop = bottom
		return
	}
	model.scrollTop = max(0, min(model.scrollTop, bottom))
}

func (r *tuiRenderer) commit(frame terminalFrame) {
	r.frame = frame
}

func buildTerminalFrame(model *tuiModel) terminalFrame {
	renderer := &tuiRenderer{}
	renderer.normalizeViewport(model)
	return projectTerminalFrame(model, renderer.prepare(model))
}

func projectTerminalFrame(model *tuiModel, prepared renderPreparation) terminalFrame {
	width := model.width
	height := model.height
	if width < 1 || height < 1 {
		return terminalFrame{}
	}

	input := prepared.input
	layout := prepared.layout
	rows := make([]styledLine, height)
	conversation := conversationViewport(prepared.conversationLines, prepared.scrollTop, layout.conversationHeight)
	copy(rows, conversation)

	rule := strings.Repeat("─", width)
	ruleStyle := lineStyle{foreground: currentTheme.thinkingColor(model.thinkingLevel)}
	if layout.topRuleRow > 0 {
		rows[layout.topRuleRow-1] = styledLine{text: rule, style: ruleStyle}
	}
	inputStyle := lineStyle{
		foreground:      currentTheme.foreground,
		background:      currentTheme.editorLine,
		paintBackground: true,
	}
	for index, line := range input.lines {
		rows[layout.inputRow-1+index] = styledLine{text: line, style: inputStyle}
	}
	if layout.bottomRuleRow > 0 {
		rows[layout.bottomRuleRow-1] = styledLine{text: rule, style: ruleStyle}
	}
	if layout.pickerRow > 0 {
		for index, line := range renderFilePicker(model, layout.pickerHeight) {
			rows[layout.pickerRow-1+index] = line
		}
	}
	if layout.statusRow > 0 {
		left, right := renderStatus(model, width)
		spinner, activity := splitActivitySpinner(model, left)
		rows[layout.statusRow-1] = styledLine{
			prefixText:       spinner,
			prefixForeground: &currentTheme.accent,
			text:             activity,
			rightText:        right,
			style:            lineStyle{foreground: currentTheme.muted},
		}
	}

	renderedRows := make([]string, height)
	plainRows := make([]string, height)
	for row, line := range rows {
		plainRows[row] = renderedLineText(line, width)
		var rendered strings.Builder
		renderLine(&rendered, row+1, width, line)
		renderedRow := rendered.String()
		if selection, ok := selectionForScreenRow(model, layout, row, plainRows[row]); ok {
			renderedRow = highlightCells(renderedRow, selection.start, selection.end)
		}
		renderedRows[row] = renderedRow
	}
	return terminalFrame{
		width:               width,
		height:              height,
		rows:                renderedRows,
		plainRows:           plainRows,
		cursorRow:           layout.inputRow + input.cursorRow,
		cursorColumn:        input.cursorColumn,
		cursorVisible:       layout.inputRow > 0,
		layout:              layout,
		conversationTop:     prepared.scrollTop,
		conversationLines:   prepared.conversationPlain,
		conversationVersion: model.conversationVersion,
	}
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
		for _, span := range fitted.spans {
			spanForeground := style.foreground
			switch span.style.foreground {
			case inlineForegroundAccent:
				spanForeground = currentTheme.accent
			case inlineForegroundError:
				spanForeground = currentTheme.error
			}
			if span.style.code {
				spanForeground = currentTheme.markdownCode
			}
			writeTextForeground(frame, spanForeground, &foreground)
			writeTextAttributes(frame, style.bold || span.style.bold, style.italic || span.style.italic, &bold, &italic)
			frame.WriteString(span.text)
		}
		writeTextForeground(frame, style.foreground, &foreground)
		writeTextAttributes(frame, style.bold, style.italic, &bold, &italic)
	}
	frame.WriteString(strings.Repeat(" ", fitted.textWidth-cellWidth(fitted.prefix)-cellWidth(fitted.text)))
	frame.WriteString(fitted.right)
	frame.WriteString(strings.Repeat(" ", fitted.rightPadding))
	frame.WriteString(ansiReset)
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

func renderInput(model *tuiModel, width, maximumHeight int) renderedInput {
	if width < 1 || maximumHeight < 1 {
		return renderedInput{}
	}
	if width <= 2 {
		return renderedInput{lines: []string{truncateCells("> ", width, false)}, cursorColumn: width}
	}

	contentWidth := width - 2
	contents := make([]string, 0, 1)
	var line strings.Builder
	lineWidth := 0
	cursorRow := 0
	cursorColumn := 3
	cursorFound := false
	flush := func() {
		contents = append(contents, line.String())
		line.Reset()
		lineWidth = 0
	}

	for index, character := range model.input {
		if character == '\n' {
			if index == model.cursor {
				cursorRow = len(contents)
				cursorColumn = min(width, 3+lineWidth)
				cursorFound = true
			}
			flush()
			continue
		}

		characterWidth := runeWidth(character)
		if lineWidth > 0 && lineWidth+characterWidth > contentWidth {
			flush()
		}
		if index == model.cursor {
			cursorRow = len(contents)
			cursorColumn = min(width, 3+lineWidth)
			cursorFound = true
		}
		line.WriteRune(character)
		lineWidth += characterWidth
	}
	if !cursorFound {
		cursorRow = len(contents)
		cursorColumn = min(width, 3+lineWidth)
	}
	flush()

	lines := make([]string, len(contents))
	for index, content := range contents {
		prefix := "  "
		if index == 0 {
			prefix = "> "
		}
		lines[index] = truncateCells(prefix+content, width, false)
	}

	height := min(maximumHeight, len(lines))
	start := 0
	if cursorRow >= height {
		start = cursorRow - height + 1
	}
	if start+height > len(lines) {
		start = len(lines) - height
	}
	return renderedInput{
		lines:        lines[start : start+height],
		cursorRow:    cursorRow - start,
		cursorColumn: cursorColumn,
	}
}

func renderFilePicker(model *tuiModel, height int) []styledLine {
	if height <= 0 {
		return nil
	}
	if len(model.filePicker.matches) == 0 {
		text := "  no matching files"
		if model.filePicker.loading {
			text = "  searching files…"
		}
		return []styledLine{{text: text, style: lineStyle{foreground: currentTheme.muted}}}
	}

	selectedPath := ""
	if model.filePicker.selected >= 0 && model.filePicker.selected < len(model.filePicker.matches) {
		selectedPath = model.filePicker.matches[model.filePicker.selected]
	}
	matches := model.visibleFilePickerMatches()
	lines := make([]styledLine, 0, min(height, len(matches)))
	for _, match := range matches[:min(height, len(matches))] {
		line := styledLine{prefixText: "  ", text: match, style: lineStyle{foreground: currentTheme.muted}}
		if match == selectedPath {
			line.prefixText = "> "
			line.prefixForeground = &currentTheme.accent
			line.style = lineStyle{
				foreground:      currentTheme.foreground,
				background:      currentTheme.selectedBackground,
				paintBackground: true,
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func renderStatus(model *tuiModel, width int) (string, string) {
	return renderStatusAt(model, width, time.Now())
}

func renderStatusAt(model *tuiModel, width int, now time.Time) (string, string) {
	activity := activityText(model)
	modelText := model.model + " (" + string(model.thinkingLevel) + ")"
	contextLong, contextShort := contextText(model.contextTokens, model.contextWindow)
	usageLong, usageShort := providerUsageText(model.providerUsage, now)

	candidates := []string{
		modelText + " · " + contextLong,
		modelText + " · " + contextShort,
		contextShort,
		"",
	}
	if usageLong != "" {
		candidates = []string{
			modelText + " · " + contextLong + " · " + usageLong,
			modelText + " · " + contextShort + " · " + usageShort,
			contextShort + " · " + usageShort,
			usageLong,
			usageShort,
			modelText + " · " + contextLong,
			contextShort,
			"",
		}
	}
	for _, right := range candidates {
		gap := 0
		if right != "" && activity != "" {
			gap = 1
		}
		if cellWidth(activity)+gap+cellWidth(right) <= width {
			return activity, right
		}
	}
	return truncateCells(activity, width, true), ""
}

func providerUsageText(usage agent.ProviderUsage, now time.Time) (string, string) {
	windows := append([]agent.UsageWindow(nil), usage.Windows...)
	sort.SliceStable(windows, func(left, right int) bool {
		return windows[left].Duration < windows[right].Duration
	})

	validWindows := 0
	for _, window := range windows {
		if window.Duration > 0 {
			validWindows++
		}
	}

	long := make([]string, 0, validWindows)
	short := make([]string, 0, validWindows)
	for _, window := range windows {
		if window.Duration <= 0 {
			continue
		}
		remaining := 100 - min(100, max(0, window.UsedPercent))
		longText := fmt.Sprintf("limit %d%%", remaining)
		shortText := longText
		if validWindows > 1 {
			label := usageWindowLabel(window.Duration)
			longText = fmt.Sprintf("%s limit %d%%", label, remaining)
			shortText = fmt.Sprintf("%s %d%%", label, remaining)
		}
		if !window.ResetsAt.IsZero() {
			reset := resetCountdown(window.ResetsAt, now)
			if reset == "now" {
				longText += " (resets now)"
				shortText += " (resets now)"
			} else {
				longText += " (resets in " + reset + ")"
				shortText += " (resets in " + reset + ")"
			}
		}
		long = append(long, longText)
		short = append(short, shortText)
	}
	return strings.Join(long, " · "), strings.Join(short, " · ")
}

func resetCountdown(reset, now time.Time) string {
	remaining := reset.Sub(now)
	if remaining <= 0 {
		return "now"
	}

	minutes := int64(remaining / time.Minute)
	if remaining%time.Minute != 0 {
		minutes++
	}
	days := minutes / (24 * 60)
	minutes -= days * 24 * 60
	hours := minutes / 60
	minutes -= hours * 60

	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", max(int64(1), minutes))
	}
}

func usageWindowLabel(duration time.Duration) string {
	switch {
	case duration%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(duration/(24*time.Hour)), 10) + "d"
	case duration%time.Hour == 0:
		return strconv.FormatInt(int64(duration/time.Hour), 10) + "h"
	case duration%time.Minute == 0:
		return strconv.FormatInt(int64(duration/time.Minute), 10) + "m"
	default:
		return duration.String()
	}
}

func splitActivitySpinner(model *tuiModel, text string) (string, string) {
	if model.activity.kind == activityReady || model.activity.kind == activityError || text == "" {
		return "", text
	}

	_, size := utf8.DecodeRuneInString(text)
	return text[:size], text[size:]
}

func activityText(model *tuiModel) string {
	label := "ready"
	switch model.activity.kind {
	case activityThinking:
		label = "thinking"
	case activityResponding:
		label = "responding"
	case activityCompacting:
		label = "compacting context"
	case activityTool:
		label = model.activity.detail
	case activityCanceling:
		label = "canceling"
	case activityError:
		label = "error: " + model.activity.detail
	}

	if model.activity.kind == activityReady || model.activity.kind == activityError {
		return label
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[model.spinner%len(frames)] + " " + label
}

func contextText(tokens, window int64) (string, string) {
	if window <= 0 {
		text := "context " + formatTokens(tokens)
		return text, text
	}

	text := fmt.Sprintf("context %d%%", tokens*100/window)
	return text, text
}

func formatTokens(tokens int64) string {
	switch {
	case tokens >= 1_000_000 && tokens%1_000_000 == 0:
		return strconv.FormatInt(tokens/1_000_000, 10) + "m"
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 1_000 && tokens%1_000 == 0:
		return strconv.FormatInt(tokens/1_000, 10) + "k"
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return strconv.FormatInt(tokens, 10)
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
