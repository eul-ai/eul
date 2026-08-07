package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const (
	ansiHideCursor              = "\x1b[?25l"
	ansiShowCursor              = "\x1b[?25h"
	ansiReset                   = "\x1b[0m"
	ansiBold                    = "\x1b[1m"
	ansiNormalIntensity         = "\x1b[22m"
	ansiItalic                  = "\x1b[3m"
	ansiNotItalic               = "\x1b[23m"
	ansiBeginSynchronizedOutput = "\x1b[?2026h"
	ansiEndSynchronizedOutput   = "\x1b[?2026l"
	ansiClearScreen             = "\x1b[2J"
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
	text      string
	rightText string
	spans     []inlineSpan
	style     lineStyle
	padding   int
}

type tuiLayout struct {
	conversationHeight int
	topRuleRow         int
	inputRow           int
	inputHeight        int
	bottomRuleRow      int
	statusRow          int
}

type renderedInput struct {
	lines        []string
	cursorRow    int
	cursorColumn int
}

type terminalFrame struct {
	width         int
	height        int
	rows          []string
	cursorRow     int
	cursorColumn  int
	cursorVisible bool
}

type tuiRenderer struct {
	previousRows  []string
	width         int
	height        int
	cursorRow     int
	cursorColumn  int
	cursorVisible bool
}

func calculateLayout(height, inputHeight int) tuiLayout {
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

	inputHeight = max(1, min(inputHeight, height-3))
	conversationHeight := height - inputHeight - 3
	return tuiLayout{
		conversationHeight: conversationHeight,
		topRuleRow:         conversationHeight + 1,
		inputRow:           conversationHeight + 2,
		inputHeight:        inputHeight,
		bottomRuleRow:      height - 1,
		statusRow:          height,
	}
}

func maximumInputHeight(height int) int {
	switch {
	case height <= 1:
		return 0
	case height < 5:
		return 1
	default:
		return height - 4
	}
}

func renderFrame(model *tuiModel) string {
	var renderer tuiRenderer
	return renderer.render(model)
}

func (r *tuiRenderer) render(model *tuiModel) string {
	next := buildTerminalFrame(model)
	if next.width < 1 || next.height < 1 {
		return ""
	}

	resized := r.width != 0 && (r.width != next.width || r.height != next.height)
	full := r.width == 0 || resized || model.forceRedraw || len(r.previousRows) != len(next.rows)
	changed := make([]int, 0, len(next.rows))
	for index, row := range next.rows {
		if full || row != r.previousRows[index] {
			changed = append(changed, index)
		}
	}
	cursorChanged := r.cursorRow != next.cursorRow || r.cursorColumn != next.cursorColumn || r.cursorVisible != next.cursorVisible
	if len(changed) == 0 && !cursorChanged {
		return ""
	}

	var output strings.Builder
	output.WriteString(ansiBeginSynchronizedOutput)
	output.WriteString(ansiHideCursor)
	if resized || model.forceRedraw {
		output.WriteString(ansiClearScreen)
	}
	for _, index := range changed {
		output.WriteString(next.rows[index])
	}
	if next.cursorVisible {
		writeCursorPosition(&output, next.cursorRow, next.cursorColumn)
		output.WriteString(ansiShowCursor)
	}
	output.WriteString(ansiEndSynchronizedOutput)

	r.previousRows = next.rows
	r.width = next.width
	r.height = next.height
	r.cursorRow = next.cursorRow
	r.cursorColumn = next.cursorColumn
	r.cursorVisible = next.cursorVisible
	model.forceRedraw = false
	return output.String()
}

func buildTerminalFrame(model *tuiModel) terminalFrame {
	width := model.width
	height := model.height
	if width < 1 || height < 1 {
		return terminalFrame{}
	}

	input := renderInput(model, width, maximumInputHeight(height))
	layout := calculateLayout(height, len(input.lines))
	rows := make([]styledLine, height)
	conversation := conversationViewport(model, width, layout.conversationHeight)
	copy(rows, conversation)

	rule := strings.Repeat("─", width)
	ruleStyle := lineStyle{foreground: currentTheme.effortColor(model.effort)}
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
	if layout.statusRow > 0 {
		left, right := renderStatus(model, width)
		rows[layout.statusRow-1] = styledLine{
			text:      left,
			rightText: right,
			style:     lineStyle{foreground: currentTheme.muted},
		}
	}

	renderedRows := make([]string, height)
	for row, line := range rows {
		var rendered strings.Builder
		renderLine(&rendered, row+1, width, line)
		renderedRows[row] = rendered.String()
	}
	return terminalFrame{
		width:         width,
		height:        height,
		rows:          renderedRows,
		cursorRow:     layout.inputRow + input.cursorRow,
		cursorColumn:  input.cursorColumn,
		cursorVisible: layout.inputRow > 0 && !model.running,
	}
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
	leftPadding := min(line.padding, width)
	rightPadding := min(line.padding, width-leftPadding)
	contentWidth := width - leftPadding - rightPadding
	right := truncateCells(line.rightText, contentWidth, false)
	textWidth := contentWidth - cellWidth(right)
	text := truncateCells(line.text, textWidth, false)
	spans := truncateInlineSpans(line.spans, textWidth)
	if len(spans) > 0 {
		text = inlineSpanText(spans)
	}

	frame.WriteString(strings.Repeat(" ", leftPadding))
	if len(spans) == 0 {
		frame.WriteString(text)
	} else {
		bold := style.bold
		italic := style.italic
		for _, span := range spans {
			writeTextAttributes(frame, style.bold || span.style.bold, style.italic || span.style.italic, &bold, &italic)
			frame.WriteString(span.text)
		}
		writeTextAttributes(frame, style.bold, style.italic, &bold, &italic)
	}
	frame.WriteString(strings.Repeat(" ", textWidth-cellWidth(text)))
	frame.WriteString(right)
	frame.WriteString(strings.Repeat(" ", rightPadding))
	frame.WriteString(ansiReset)
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

func conversationViewport(model *tuiModel, width, height int) []styledLine {
	if height <= 0 {
		return nil
	}

	lines := modelConversationLines(model, width)
	bottom := len(lines) - height
	if bottom < 0 {
		bottom = 0
	}
	if model.following {
		model.scrollTop = bottom
	} else if model.scrollTop > bottom {
		model.scrollTop = bottom
	}
	if model.scrollTop < 0 {
		model.scrollTop = 0
	}

	visible := make([]styledLine, height)
	end := model.scrollTop + height
	if end > len(lines) {
		end = len(lines)
	}
	if model.scrollTop < end {
		copy(visible, lines[model.scrollTop:end])
	}
	return visible
}

func modelConversationLines(model *tuiModel, width int) []styledLine {
	if model.conversationDirty || model.wrappedWidth != width {
		lines := conversationLines(model.blocks, width)
		model.conversationLines = make([]styledLine, 0, len(lines)+conversationVerticalPadding*2)
		model.conversationLines = append(model.conversationLines, make([]styledLine, conversationVerticalPadding)...)
		model.conversationLines = append(model.conversationLines, lines...)
		model.conversationLines = append(model.conversationLines, make([]styledLine, conversationVerticalPadding)...)
		model.wrappedWidth = width
		model.conversationDirty = false
	}
	return model.conversationLines
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
		}
		if block.kind == blockAssistant || block.kind == blockReasoning {
			for _, line := range wrapInlineMarkdown(text, contentWidth) {
				lines = append(lines, styledLine{text: line.text, spans: line.spans, style: style, padding: padding})
			}
		} else {
			for _, line := range wrapText(text, contentWidth) {
				lines = append(lines, styledLine{text: line, style: style, padding: padding})
			}
		}
		if isToolBlock(block.kind) {
			lines = append(lines, styledLine{style: style, padding: padding})
		}
		if index < len(blocks)-1 {
			lines = append(lines, styledLine{})
		}
	}
	return lines
}

func isToolBlock(kind blockKind) bool {
	return kind == blockToolPending || kind == blockTool || kind == blockToolError
}

func blockPresentation(kind blockKind) lineStyle {
	switch kind {
	case blockUser, blockAssistant:
		return lineStyle{foreground: currentTheme.foreground}
	case blockReasoning:
		return lineStyle{foreground: currentTheme.muted, italic: true}
	case blockContext:
		return lineStyle{foreground: currentTheme.muted}
	case blockToolPending:
		return lineStyle{foreground: currentTheme.accent, background: currentTheme.toolPendingBackground, paintBackground: true}
	case blockTool:
		return lineStyle{foreground: currentTheme.accent, background: currentTheme.toolSuccessBackground, paintBackground: true}
	case blockToolError:
		return lineStyle{foreground: currentTheme.error, background: currentTheme.toolErrorBackground, paintBackground: true}
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

func renderStatus(model *tuiModel, width int) (string, string) {
	activity := activityText(model)
	modelText := model.model + " (" + model.effort + ")"
	contextLong, contextShort := contextText(model.contextTokens, model.contextWindow)

	candidates := []string{
		modelText + " · " + contextLong,
		modelText + " · " + contextShort,
		contextShort,
		"",
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

	percentage := tokens * 100 / window
	long := fmt.Sprintf("context %s/%s (%d%%)", formatTokens(tokens), formatTokens(window), percentage)
	short := fmt.Sprintf("context %d%%", percentage)
	return long, short
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

func scrollConversation(model *tuiModel, direction int) {
	input := renderInput(model, model.width, maximumInputHeight(model.height))
	layout := calculateLayout(model.height, len(input.lines))
	page := layout.conversationHeight
	if page <= 0 {
		return
	}

	lines := modelConversationLines(model, model.width)
	bottom := len(lines) - page
	if bottom < 0 {
		bottom = 0
	}
	if model.following {
		model.scrollTop = bottom
	}
	model.following = false
	model.scrollTop += direction * page
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
