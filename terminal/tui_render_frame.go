package terminal

import (
	"fmt"
	"slices"
	"strings"
)

type terminalFrame struct {
	width                  int
	height                 int
	rows                   []string
	plainRows              []string
	cursorRow              int
	cursorColumn           int
	cursorVisible          bool
	layout                 tuiLayout
	conversationTop        int
	conversationLines      []string
	conversationSeparators []string
	conversationVersion    uint64
	conversationTruncated  bool
}

type renderPreparation struct {
	input                  renderedInput
	layout                 tuiLayout
	conversationLines      []styledLine
	conversationPlain      []string
	conversationSeparators []string
	conversationTruncated  bool
	scrollTop              int
}

const resizeHistoryRows = 200

func (r *tuiRenderer) prepare(model *tuiModel) renderPreparation {
	input, layout := modelInputLayout(model)
	minimumRows := layout.conversationHeight + resizeHistoryRows
	conversationChanged := r.conversationWidth != model.width || r.conversationVersion != model.conversationVersion
	conversationTooShort := r.conversationBlockStart > 0 && cachedConversationRows(r.conversationBlocks) < minimumRows
	if conversationChanged || conversationTooShort || model.historyExpansionRequested {
		r.prepareConversation(model, layout.conversationHeight, minimumRows)
	}

	return renderPreparation{
		input:                  input,
		layout:                 layout,
		conversationLines:      r.conversationLines,
		conversationPlain:      r.conversationPlain,
		conversationSeparators: r.conversationSeparators,
		conversationTruncated:  r.conversationBlockStart > 0,
		scrollTop:              model.scrollTop,
	}
}

func (r *tuiRenderer) prepareConversation(model *tuiModel, conversationHeight, minimumRows int) {
	widthChanged := r.conversationWidth != model.width
	initialLayout := !r.conversationPrepared
	preserveTailPosition := widthChanged && r.conversationBlockStart > 0 && !model.following
	distanceFromBottom := 0
	if preserveTailPosition {
		oldBottom := max(0, len(r.conversationLines)-r.frame.layout.conversationHeight)
		distanceFromBottom = oldBottom - model.scrollTop
	}
	if widthChanged {
		switch {
		case initialLayout || !model.following || model.historyExpansionRequested:
			r.renderAllConversationBlocks(model)
		default:
			r.renderConversationTail(model, minimumRows)
		}
	} else {
		r.updateConversationBlocks(model)

		complete := model.historyExpansionRequested
		if complete || r.conversationBlockStart > 0 && cachedConversationRows(r.conversationBlocks) < minimumRows {
			addedRows := r.prependConversationBlocks(model, minimumRows, complete)
			shiftConversationRows(model, addedRows)
		}
	}
	model.historyExpansionRequested = false

	r.flattenConversation(model)
	if preserveTailPosition {
		newBottom := max(0, len(r.conversationLines)-conversationHeight)
		model.scrollTop = newBottom - distanceFromBottom
	}
	r.conversationWidth = model.width
	r.conversationVersion = model.conversationVersion
	r.conversationPrepared = true
}

func (r *tuiRenderer) renderAllConversationBlocks(model *tuiModel) {
	r.conversationBlocks = make([]renderedConversationBlock, len(model.blocks))
	for index, block := range model.blocks {
		r.conversationBlocks[index] = renderConversationBlock(block, model.width)
	}
	r.conversationBlockStart = 0
}

func (r *tuiRenderer) renderConversationTail(model *tuiModel, minimumRows int) {
	r.conversationBlocks = nil
	r.conversationBlockStart = len(model.blocks)
	r.prependConversationBlocks(model, minimumRows, false)
}

func (r *tuiRenderer) updateConversationBlocks(model *tuiModel) {
	if r.conversationBlockStart > len(model.blocks) {
		clear(r.conversationBlocks)
		r.conversationBlocks = nil
		r.conversationBlockStart = len(model.blocks)
	}

	cachedCount := len(model.blocks) - r.conversationBlockStart
	if len(r.conversationBlocks) > cachedCount {
		clear(r.conversationBlocks[cachedCount:])
		r.conversationBlocks = r.conversationBlocks[:cachedCount]
	}
	for blockIndex := r.conversationBlockStart; blockIndex < len(model.blocks); blockIndex++ {
		cacheIndex := blockIndex - r.conversationBlockStart
		block := model.blocks[blockIndex]
		if cacheIndex == len(r.conversationBlocks) {
			r.conversationBlocks = append(r.conversationBlocks, renderConversationBlock(block, model.width))
			continue
		}
		if !conversationBlocksEqual(r.conversationBlocks[cacheIndex].block, block) {
			r.conversationBlocks[cacheIndex] = renderConversationBlock(block, model.width)
		}
	}
}

func (r *tuiRenderer) prependConversationBlocks(model *tuiModel, minimumRows int, complete bool) int {
	oldBlockCount := len(r.conversationBlocks)
	rows := cachedConversationRows(r.conversationBlocks)
	var reversed []renderedConversationBlock
	for r.conversationBlockStart > 0 && (complete || rows < minimumRows) {
		r.conversationBlockStart--
		rendered := renderConversationBlock(model.blocks[r.conversationBlockStart], model.width)
		reversed = append(reversed, rendered)
		rows += len(rendered.lines)
		if oldBlockCount > 0 || len(reversed) > 1 {
			rows++
		}
	}
	if len(reversed) == 0 {
		return 0
	}

	slices.Reverse(reversed)
	addedRows := 0
	if oldBlockCount > 0 {
		for _, rendered := range reversed {
			addedRows += len(rendered.lines) + 1
		}
	}
	r.conversationBlocks = append(reversed, r.conversationBlocks...)
	return addedRows
}

func cachedConversationRows(blocks []renderedConversationBlock) int {
	rows := conversationVerticalPadding * 2
	for index, block := range blocks {
		rows += len(block.lines)
		if index < len(blocks)-1 {
			rows++
		}
	}
	return rows
}

func shiftConversationRows(model *tuiModel, rows int) {
	if rows == 0 {
		return
	}
	if !model.following {
		model.scrollTop += rows
	}
	if model.selection.set && model.selection.anchor.conversation {
		model.selection.anchor.row += rows
		model.selection.focus.row += rows
	}
}

func (r *tuiRenderer) flattenConversation(model *tuiModel) {
	lineCapacity := len(r.conversationLines)
	plainCapacity := len(r.conversationPlain)
	lines := make([]styledLine, 0, lineCapacity)
	plain := make([]string, 0, plainCapacity)
	separators := make([]string, 0, lineCapacity)
	blankPlain := renderedLineText(styledLine{}, model.width)
	appendBlank := func() {
		lines = append(lines, styledLine{})
		plain = append(plain, blankPlain)
		separators = append(separators, "\n")
	}
	for range conversationVerticalPadding {
		appendBlank()
	}

	pendingSteering := model.pendingSteering()
	totalBlocks := len(r.conversationBlocks) + len(pendingSteering)
	appendedBlocks := 0
	appendBlock := func(rendered renderedConversationBlock) {
		lines = append(lines, rendered.lines...)
		plain = append(plain, rendered.plain...)
		separators = append(separators, rendered.separators...)
		appendedBlocks++
		if appendedBlocks < totalBlocks {
			appendBlank()
		}
	}
	for _, rendered := range r.conversationBlocks {
		appendBlock(rendered)
	}
	for _, content := range pendingSteering {
		appendBlock(renderConversationBlock(conversationBlock{kind: blockInfo, text: "Queued: " + displayContent(content) + " (alt+↑ to restore)"}, model.width))
	}
	for range conversationVerticalPadding {
		appendBlank()
	}

	r.conversationLines = lines
	r.conversationPlain = plain
	r.conversationSeparators = separators
}

func renderConversationBlock(block conversationBlock, width int) renderedConversationBlock {
	lines := conversationBlockLines(block, width)
	plain := make([]string, len(lines))
	separators := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = renderedLineText(line, width)
		separators[index] = line.breakBefore.sourceSeparator()
	}
	block.tool = block.tool.Clone()
	block.content = cloneTerminalContent(block.content)
	return renderedConversationBlock{block: block, lines: lines, plain: plain, separators: separators}
}

func conversationBlocksEqual(left, right conversationBlock) bool {
	return left.kind == right.kind &&
		left.text == right.text &&
		contentEqual(left.content, right.content) &&
		left.toolCallID == right.toolCallID &&
		left.toolOutcome == right.toolOutcome &&
		left.tool.Equal(right.tool)
}

func viewportTop(model *tuiModel, prepared renderPreparation) int {
	bottom := max(0, len(prepared.conversationLines)-prepared.layout.conversationHeight)
	if model.following {
		return bottom
	}
	return max(0, min(model.scrollTop, bottom))
}

func normalizeViewport(model *tuiModel, renderer *tuiRenderer) {
	model.scrollTop = viewportTop(model, renderer.prepare(model))
}

func buildTerminalFrame(model *tuiModel) terminalFrame {
	renderer := &tuiRenderer{}
	prepared := renderer.prepare(model)
	prepared.scrollTop = viewportTop(model, prepared)
	return projectTerminalFrame(model, prepared)
}

func projectTerminalFrame(model *tuiModel, prepared renderPreparation) terminalFrame {
	width := model.width
	height := model.height
	if width < 1 || height < 1 {
		return terminalFrame{}
	}

	rows := composeFrameRows(model, prepared)
	renderedRows, plainRows := encodeFrameRows(model, prepared.layout, rows)
	return terminalFrame{
		width:                  width,
		height:                 height,
		rows:                   renderedRows,
		plainRows:              plainRows,
		cursorRow:              prepared.layout.inputRow + prepared.input.cursorRow,
		cursorColumn:           prepared.input.cursorColumn,
		cursorVisible:          prepared.layout.inputRow > 0 && !model.permission.active(),
		layout:                 prepared.layout,
		conversationTop:        prepared.scrollTop,
		conversationLines:      prepared.conversationPlain,
		conversationSeparators: prepared.conversationSeparators,
		conversationVersion:    model.conversationVersion,
		conversationTruncated:  prepared.conversationTruncated,
	}
}

func composeFrameRows(model *tuiModel, prepared renderPreparation) []styledLine {
	width := model.width
	layout := prepared.layout
	rows := make([]styledLine, model.height)
	copy(rows, conversationViewport(prepared.conversationLines, prepared.scrollTop, layout.conversationHeight))

	if layout.subagentRow > 0 {
		for index, line := range renderSubagents(model, layout.subagentHeight) {
			rows[layout.subagentRow-1+index] = line
		}
	}

	rule := strings.Repeat("─", width)
	ruleStyle := lineStyle{foreground: currentTheme.thinkingColor(model.thinkingLevel)}
	if model.permission.active() {
		ruleStyle.foreground = currentTheme.orange
	}
	if layout.topRuleRow > 0 {
		topRule := styledLine{text: rule, style: ruleStyle}
		if model.permission.active() {
			topRule.text = permissionRule(model.permission, width)
		} else {
			bottom := max(0, len(prepared.conversationLines)-layout.conversationHeight)
			if prepared.scrollTop < bottom {
				indicator := truncateCells(" ↓ more (alt+↓) ", width, false)
				remaining := width - cellWidth(indicator)
				left := remaining / 2
				topRule.text = strings.Repeat("─", left) + indicator + strings.Repeat("─", remaining-left)
			}
		}
		rows[layout.topRuleRow-1] = topRule
	}
	inputStyle := lineStyle{foreground: currentTheme.foreground}
	for index, line := range prepared.input.lines {
		row := styledLine{text: line, style: inputStyle}
		if len(prepared.input.styledLines) > index {
			row = prepared.input.styledLines[index]
		}
		rows[layout.inputRow-1+index] = row
	}
	if layout.bottomRuleRow > 0 {
		rows[layout.bottomRuleRow-1] = styledLine{text: rule, style: ruleStyle}
	}
	if layout.pickerRow > 0 {
		for index, line := range renderPicker(model, layout.pickerHeight) {
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
	return rows
}

func permissionRule(permission permissionModel, width int) string {
	label := permission.title
	if permission.total > 1 {
		label += fmt.Sprintf(" (%d of %d)", permission.index, permission.total)
	}
	prefix := "── " + label + " "
	prefix = truncateCells(prefix, width, true)
	return prefix + strings.Repeat("─", max(0, width-cellWidth(prefix)))
}

func encodeFrameRows(model *tuiModel, layout tuiLayout, rows []styledLine) ([]string, []string) {
	renderedRows := make([]string, len(rows))
	plainRows := make([]string, len(rows))
	for row, line := range rows {
		plainRows[row] = renderedLineText(line, model.width)
		var rendered strings.Builder
		renderLine(&rendered, row+1, model.width, line)
		renderedRow := rendered.String()
		if selection, ok := selectionForScreenRow(model, layout, row, plainRows[row]); ok {
			renderedRow = highlightCells(renderedRow, selection.start, selection.end)
		}
		renderedRows[row] = renderedRow
	}
	return renderedRows, plainRows
}
