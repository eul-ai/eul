package terminal

import "strings"

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
		width:               width,
		height:              height,
		rows:                renderedRows,
		plainRows:           plainRows,
		cursorRow:           prepared.layout.inputRow + prepared.input.cursorRow,
		cursorColumn:        prepared.input.cursorColumn,
		cursorVisible:       prepared.layout.inputRow > 0,
		layout:              prepared.layout,
		conversationTop:     prepared.scrollTop,
		conversationLines:   prepared.conversationPlain,
		conversationVersion: model.conversationVersion,
	}
}

func composeFrameRows(model *tuiModel, prepared renderPreparation) []styledLine {
	width := model.width
	layout := prepared.layout
	rows := make([]styledLine, model.height)
	copy(rows, conversationViewport(prepared.conversationLines, prepared.scrollTop, layout.conversationHeight))

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
	for index, line := range prepared.input.lines {
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
	return rows
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
