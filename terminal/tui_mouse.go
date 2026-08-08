package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const mouseWheelScrollLines = 3

type selectionPoint struct {
	row          int
	column       int
	conversation bool
}

type textSelection struct {
	anchor   selectionPoint
	focus    selectionPoint
	set      bool
	pressing bool
}

type selectionBounds struct {
	start selectionPoint
	end   selectionPoint
}

type cellRange struct {
	start int
	end   int
}

func reduceMouse(model *tuiModel, event mouseEvent) tuiAction {
	switch event.kind {
	case mouseWheelUp:
		scrollConversationBy(model, -mouseWheelScrollLines)
	case mouseWheelDown:
		scrollConversationBy(model, mouseWheelScrollLines)
	case mousePress:
		point := selectionPointAt(model, event, false)
		model.selection = textSelection{anchor: point, focus: point, set: true, pressing: true}
	case mouseDrag:
		if model.selection.pressing {
			model.selection.focus = selectionPointAt(model, event, model.selection.anchor.conversation)
		}
	case mouseRelease:
		if !model.selection.pressing {
			return tuiAction{}
		}
		model.selection.pressing = false
		model.selection.focus = selectionPointAt(model, event, model.selection.anchor.conversation)
		if _, ok := model.selection.bounds(); !ok {
			model.selection = textSelection{}
			return tuiAction{}
		}
		text := selectedText(model)
		if text == "" {
			model.selection = textSelection{}
			return tuiAction{}
		}
		return tuiAction{kind: tuiActionCopy, text: text}
	}
	return tuiAction{}
}

func selectionPointAt(model *tuiModel, event mouseEvent, forceConversation bool) selectionPoint {
	_, layout := modelInputLayout(model)
	conversationViewport(model, model.width, layout.conversationHeight)

	column := max(0, min(model.width-1, event.column))
	if forceConversation || event.row >= 0 && event.row < layout.conversationHeight {
		viewportRow := max(0, min(layout.conversationHeight-1, event.row))
		contentRow := model.scrollTop + viewportRow
		contentRow = max(0, min(len(modelConversationLines(model, model.width))-1, contentRow))
		return selectionPoint{row: contentRow, column: column, conversation: true}
	}

	row := max(0, min(model.height-1, event.row))
	return selectionPoint{row: row, column: column}
}

func (selection textSelection) bounds() (selectionBounds, bool) {
	if !selection.set || selection.anchor.conversation != selection.focus.conversation {
		return selectionBounds{}, false
	}
	if selection.anchor.row == selection.focus.row && selection.anchor.column == selection.focus.column {
		return selectionBounds{}, false
	}

	anchorBeforeFocus := selection.anchor.row < selection.focus.row ||
		selection.anchor.row == selection.focus.row && selection.anchor.column < selection.focus.column
	if anchorBeforeFocus {
		return selectionBounds{start: selection.anchor, end: selection.focus}, true
	}
	return selectionBounds{start: selection.focus, end: selection.anchor}, true
}

func selectedText(model *tuiModel) string {
	bounds, ok := model.selection.bounds()
	if !ok {
		return ""
	}

	var lines []string
	if bounds.start.conversation {
		styled := modelConversationLines(model, model.width)
		lines = make([]string, len(styled))
		for index, line := range styled {
			lines[index] = renderedLineText(line, model.width)
		}
	} else {
		lines = buildTerminalFrame(model).plainRows
	}
	return selectedTextFromLines(lines, bounds)
}

func selectedTextFromLines(lines []string, bounds selectionBounds) string {
	if len(lines) == 0 || bounds.start.row >= len(lines) || bounds.end.row < 0 {
		return ""
	}

	startRow := max(0, bounds.start.row)
	endRow := min(len(lines)-1, bounds.end.row)
	selected := make([]string, 0, endRow-startRow+1)
	for row := startRow; row <= endRow; row++ {
		columns := selectedColumns(lines[row], row, bounds)
		line := sliceCells(lines[row], columns.start, columns.end)
		selected = append(selected, strings.TrimRightFunc(line, unicode.IsSpace))
	}
	return strings.Join(selected, "\n")
}

func selectedColumns(line string, row int, bounds selectionBounds) cellRange {
	width := cellWidth(line)
	start := 0
	end := width
	if row == bounds.start.row {
		start, _ = cellRangeAt(line, bounds.start.column)
	}
	if row == bounds.end.row {
		_, end = cellRangeAt(line, bounds.end.column)
	}
	return cellRange{start: max(0, min(width, start)), end: max(0, min(width, end))}
}

func cellRangeAt(line string, column int) (int, int) {
	column = max(0, column)
	position := 0
	for _, character := range line {
		width := runeWidth(character)
		if width == 0 {
			continue
		}
		if column < position+width {
			return position, position + width
		}
		position += width
	}
	return position, position
}

func sliceCells(value string, start, end int) string {
	if start >= end {
		return ""
	}

	var result strings.Builder
	position := 0
	selectedBase := false
	for _, character := range value {
		width := runeWidth(character)
		if width == 0 {
			if selectedBase {
				result.WriteRune(character)
			}
			continue
		}

		next := position + width
		selectedBase = position < end && next > start
		if selectedBase {
			result.WriteRune(character)
		}
		position = next
	}
	return result.String()
}

func highlightCells(value string, start, end int) string {
	if start >= end {
		return value
	}

	var result strings.Builder
	position := 0
	highlighted := false
	for index := 0; index < len(value); {
		if value[index] == '\x1b' {
			length := terminalEscapeLength(value[index:])
			if length > 0 {
				sequence := value[index : index+length]
				result.WriteString(sequence)
				if highlighted && strings.HasSuffix(sequence, "m") {
					result.WriteString(ansiReverse)
				}
				index += length
				continue
			}
		}

		character, size := utf8.DecodeRuneInString(value[index:])
		width := runeWidth(character)
		selected := highlighted
		if width > 0 {
			selected = position < end && position+width > start
		}
		if selected != highlighted {
			if selected {
				result.WriteString(ansiReverse)
			} else {
				result.WriteString(ansiNotReverse)
			}
			highlighted = selected
		}
		result.WriteString(value[index : index+size])
		position += width
		index += size
	}
	if highlighted {
		result.WriteString(ansiNotReverse)
	}
	return result.String()
}

func terminalEscapeLength(value string) int {
	if len(value) < 3 || value[0] != '\x1b' || value[1] != '[' {
		return 0
	}
	for index := 2; index < len(value); index++ {
		if value[index] >= 0x40 && value[index] <= 0x7e {
			return index + 1
		}
	}
	return 0
}

func selectionForScreenRow(model *tuiModel, layout tuiLayout, row int, line string) (cellRange, bool) {
	bounds, ok := model.selection.bounds()
	if !ok {
		return cellRange{}, false
	}

	selectionRow := row
	if bounds.start.conversation {
		if row < 0 || row >= layout.conversationHeight {
			return cellRange{}, false
		}
		selectionRow = model.scrollTop + row
	}
	if selectionRow < bounds.start.row || selectionRow > bounds.end.row {
		return cellRange{}, false
	}

	line = strings.TrimRightFunc(line, unicode.IsSpace)
	columns := selectedColumns(line, selectionRow, bounds)
	return columns, columns.end > columns.start
}
