package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const (
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
	ansiReset      = "\x1b[0m"
	ansiItalic     = "\x1b[3m"
	toolPadding    = 1
)

type lineStyle struct {
	foreground      terminalColor
	background      terminalColor
	paintBackground bool
	italic          bool
}

type styledLine struct {
	text      string
	rightText string
	style     lineStyle
	padding   int
}

type tuiLayout struct {
	conversationHeight int
	topRuleRow         int
	inputRow           int
	bottomRuleRow      int
	statusRow          int
}

func calculateLayout(height int) tuiLayout {
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
			statusRow:          height,
		}
	}

	return tuiLayout{
		conversationHeight: height - 4,
		topRuleRow:         height - 3,
		inputRow:           height - 2,
		bottomRuleRow:      height - 1,
		statusRow:          height,
	}
}

func renderFrame(model *tuiModel) string {
	width := model.width
	height := model.height
	if width < 1 || height < 1 {
		return ""
	}

	layout := calculateLayout(height)
	rows := make([]styledLine, height)
	conversation := conversationViewport(model, width, layout.conversationHeight)
	copy(rows, conversation)

	rule := strings.Repeat("─", width)
	ruleStyle := lineStyle{foreground: currentTheme.effortColor(model.effort)}
	if layout.topRuleRow > 0 {
		rows[layout.topRuleRow-1] = styledLine{text: rule, style: ruleStyle}
	}
	cursorColumn := 1
	if layout.inputRow > 0 {
		input, column := renderInput(model, width)
		rows[layout.inputRow-1] = styledLine{
			text: input,
			style: lineStyle{
				foreground:      currentTheme.foreground,
				background:      currentTheme.editorLine,
				paintBackground: true,
			},
		}
		cursorColumn = column
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

	var frame strings.Builder
	frame.WriteString(ansiHideCursor)
	for row, line := range rows {
		renderLine(&frame, row+1, width, line)
	}
	if layout.inputRow > 0 && !model.running {
		frame.WriteString("\x1b[")
		frame.WriteString(strconv.Itoa(layout.inputRow))
		frame.WriteByte(';')
		frame.WriteString(strconv.Itoa(cursorColumn))
		frame.WriteByte('H')
		frame.WriteString(ansiShowCursor)
	}
	return frame.String()
}

func renderLine(frame *strings.Builder, row, width int, line styledLine) {
	base := lineStyle{foreground: currentTheme.foreground}
	frame.WriteString("\x1b[")
	frame.WriteString(strconv.Itoa(row))
	frame.WriteString(";1H")
	frame.WriteString(ansiColors(base.foreground, base.background, base.paintBackground))
	frame.WriteString(strings.Repeat(" ", width))

	style := line.style
	if style == (lineStyle{}) {
		style = base
	}
	frame.WriteString("\x1b[")
	frame.WriteString(strconv.Itoa(row))
	frame.WriteString(";1H")
	frame.WriteString(ansiColors(style.foreground, style.background, style.paintBackground))
	if style.italic {
		frame.WriteString(ansiItalic)
	}
	contentWidth := width - line.padding*2
	if contentWidth < 0 {
		contentWidth = 0
	}
	text := truncateCells(line.text, contentWidth, false)
	frame.WriteString(strings.Repeat(" ", line.padding))
	frame.WriteString(text)
	frame.WriteString(strings.Repeat(" ", width-line.padding-cellWidth(text)))
	if line.rightText != "" {
		right := truncateCells(line.rightText, width, false)
		frame.WriteString("\x1b[")
		frame.WriteString(strconv.Itoa(row))
		frame.WriteByte(';')
		frame.WriteString(strconv.Itoa(width - cellWidth(right) + 1))
		frame.WriteByte('H')
		frame.WriteString(right)
	}
	frame.WriteString(ansiReset)
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
		model.conversationLines = conversationLines(model.blocks, width)
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

		padding := 0
		contentWidth := width
		if isToolBlock(block.kind) {
			padding = toolPadding
			contentWidth -= padding * 2
			if contentWidth < 1 {
				contentWidth = 1
			}
			lines = append(lines, styledLine{style: style, padding: padding})
		}
		for _, line := range wrapText(text, contentWidth) {
			lines = append(lines, styledLine{text: line, style: style, padding: padding})
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

func renderInput(model *tuiModel, width int) (string, int) {
	if width <= 2 {
		return truncateCells("> ", width, false), width
	}

	available := width - 2
	start := 0
	for start < model.cursor && runesWidth(model.input[start:model.cursor]) > available-1 {
		start++
	}

	var visible strings.Builder
	used := 0
	for _, character := range model.input[start:] {
		characterWidth := runeWidth(character)
		if used+characterWidth > available {
			break
		}
		visible.WriteRune(character)
		used += characterWidth
	}

	cursorColumn := 3 + runesWidth(model.input[start:model.cursor])
	if cursorColumn > width {
		cursorColumn = width
	}
	return "> " + visible.String(), cursorColumn
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
	layout := calculateLayout(model.height)
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
