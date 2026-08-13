package terminal

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type terminalFrame struct {
	width                     int
	height                    int
	rows                      []string
	plainRows                 []string
	cursorRow                 int
	cursorColumn              int
	cursorVisible             bool
	layout                    tuiLayout
	conversationTop           int
	conversationLines         []string
	conversationContinuations []bool
	conversationVersion       uint64
}

type renderPreparation struct {
	input                     renderedInput
	layout                    tuiLayout
	conversationLines         []styledLine
	conversationPlain         []string
	conversationContinuations []bool
	scrollTop                 int
}

func (r *tuiRenderer) prepare(model *tuiModel) renderPreparation {
	input, layout := modelInputLayout(model)
	if r.conversationWidth != model.width || r.conversationVersion != model.conversationVersion {
		r.prepareConversation(model)
	}

	return renderPreparation{
		input:                     input,
		layout:                    layout,
		conversationLines:         r.conversationLines,
		conversationPlain:         r.conversationPlain,
		conversationContinuations: r.conversationContinuations,
		scrollTop:                 model.scrollTop,
	}
}

func (r *tuiRenderer) prepareConversation(model *tuiModel) {
	if r.conversationWidth != model.width {
		r.conversationBlocks = nil
	}
	if len(r.conversationBlocks) > len(model.blocks) {
		clear(r.conversationBlocks[len(model.blocks):])
		r.conversationBlocks = r.conversationBlocks[:len(model.blocks)]
	}
	for index, block := range model.blocks {
		if index == len(r.conversationBlocks) {
			r.conversationBlocks = append(r.conversationBlocks, renderConversationBlock(block, model.width))
			continue
		}
		if !conversationBlocksEqual(r.conversationBlocks[index].block, block) {
			r.conversationBlocks[index] = renderConversationBlock(block, model.width)
		}
	}

	lineCapacity := len(r.conversationLines)
	plainCapacity := len(r.conversationPlain)
	lines := make([]styledLine, 0, lineCapacity)
	plain := make([]string, 0, plainCapacity)
	continuations := make([]bool, 0, lineCapacity)
	blankPlain := renderedLineText(styledLine{}, model.width)
	appendBlank := func() {
		lines = append(lines, styledLine{})
		plain = append(plain, blankPlain)
		continuations = append(continuations, false)
	}
	for range conversationVerticalPadding {
		appendBlank()
	}

	totalBlocks := len(model.blocks) + len(model.steering)
	appendedBlocks := 0
	appendBlock := func(rendered renderedConversationBlock) {
		lines = append(lines, rendered.lines...)
		plain = append(plain, rendered.plain...)
		continuations = append(continuations, rendered.continuations...)
		appendedBlocks++
		if appendedBlocks < totalBlocks {
			appendBlank()
		}
	}
	for _, rendered := range r.conversationBlocks {
		appendBlock(rendered)
	}
	for _, message := range model.steering {
		appendBlock(renderConversationBlock(conversationBlock{kind: blockInfo, text: "Queued: " + message}, model.width))
	}
	for range conversationVerticalPadding {
		appendBlank()
	}

	r.conversationLines = lines
	r.conversationPlain = plain
	r.conversationContinuations = continuations
	r.conversationWidth = model.width
	r.conversationVersion = model.conversationVersion
}

func renderConversationBlock(block conversationBlock, width int) renderedConversationBlock {
	lines := conversationBlockLines(block, width)
	plain := make([]string, len(lines))
	continuations := make([]bool, len(lines))
	for index, line := range lines {
		plain[index] = renderedLineText(line, width)
		continuations[index] = line.continuation
	}
	block.tool = block.tool.Clone()
	block.content = cloneTerminalContent(block.content)
	return renderedConversationBlock{block: block, lines: lines, plain: plain, continuations: continuations}
}

func contentEqual(left, right []agent.ContentPart) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Kind != right[index].Kind || left[index].Text != right[index].Text {
			return false
		}
		leftImage := left[index].Image
		rightImage := right[index].Image
		switch {
		case leftImage == nil && rightImage == nil:
		case leftImage == nil || rightImage == nil:
			return false
		case leftImage.MediaType != rightImage.MediaType || !bytes.Equal(leftImage.Data, rightImage.Data):
			return false
		}
	}
	return true
}

func conversationBlocksEqual(left, right conversationBlock) bool {
	return left.kind == right.kind &&
		left.text == right.text &&
		contentEqual(left.content, right.content) &&
		left.toolCallID == right.toolCallID &&
		left.toolOutcome == right.toolOutcome &&
		left.tool.Equal(right.tool)
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

	rows := composeFrameRows(model, prepared)
	renderedRows, plainRows := encodeFrameRows(model, prepared.layout, rows)
	return terminalFrame{
		width:                     width,
		height:                    height,
		rows:                      renderedRows,
		plainRows:                 plainRows,
		cursorRow:                 prepared.layout.inputRow + prepared.input.cursorRow,
		cursorColumn:              prepared.input.cursorColumn,
		cursorVisible:             prepared.layout.inputRow > 0 && !model.permission.active(),
		layout:                    prepared.layout,
		conversationTop:           prepared.scrollTop,
		conversationLines:         prepared.conversationPlain,
		conversationContinuations: prepared.conversationContinuations,
		conversationVersion:       model.conversationVersion,
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
				indicator := truncateCells("↓ more", width, false)
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
