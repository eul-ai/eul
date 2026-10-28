package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/terminal/keys"
	"github.com/eul-ai/eul/terminal/text"
)

const mouseWheelScrollLines = 1

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

func reduceMouse(model *tuiModel, event keys.Mouse, frame terminalFrame) tuiAction {
	switch event.Kind {
	case keys.MouseWheelUp:
		scrollConversationBy(model, -mouseWheelScrollLines, frame)
	case keys.MouseWheelDown:
		scrollConversationBy(model, mouseWheelScrollLines, frame)
	case keys.MousePress:
		point := selectionPointAt(event, false, frame)
		model.selection = textSelection{anchor: point, focus: point, set: true, pressing: true}
	case keys.MouseDrag:
		if model.selection.pressing {
			model.selection.focus = selectionPointAt(event, model.selection.anchor.conversation, frame)
		}
	case keys.MouseRelease:
		if !model.selection.pressing {
			return tuiAction{}
		}
		model.selection.pressing = false
		model.selection.focus = selectionPointAt(event, model.selection.anchor.conversation, frame)
		if _, ok := model.selection.bounds(); !ok {
			model.selection = textSelection{}
			return tuiAction{}
		}
		text := selectedText(model, frame)
		model.selection = textSelection{}
		if text == "" {
			return tuiAction{}
		}
		return tuiAction{kind: tuiActionCopy, text: text}
	}
	return tuiAction{}
}

func selectionPointAt(event keys.Mouse, forceConversation bool, frame terminalFrame) selectionPoint {
	layout := frame.layout
	column := max(0, min(frame.width-1, event.Column))
	if forceConversation || event.Row >= 0 && event.Row < layout.conversationHeight {
		viewportRow := max(0, min(layout.conversationHeight-1, event.Row))
		contentRow := frame.conversationTop + viewportRow
		contentRow = max(0, min(len(frame.conversationLines)-1, contentRow))
		return selectionPoint{row: contentRow, column: column, conversation: true}
	}

	row := max(0, min(frame.height-1, event.Row))
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

func selectedText(model *tuiModel, frame terminalFrame) string {
	bounds, ok := model.selection.bounds()
	if !ok {
		return ""
	}

	if bounds.start.conversation {
		return selectedConversationText(frame, bounds)
	}
	return selectedTextFromLines(frame.plainRows, bounds)
}

func selectedConversationText(frame terminalFrame, bounds selectionBounds) string {
	lines := frame.conversationLines
	if len(lines) == 0 || bounds.start.row >= len(lines) || bounds.end.row < 0 {
		return ""
	}

	startRow := max(0, bounds.start.row)
	endRow := min(len(lines)-1, bounds.end.row)
	var selected strings.Builder
	for row := startRow; row <= endRow; row++ {
		if row > startRow {
			selected.WriteString(conversationSeparator(frame.conversationSeparators, row))
		}
		if markdown, ok := conversationMarkdownAt(frame.conversationMarkdown, row); ok {
			lastRow := min(endRow, markdown.row+markdown.rows-1)
			selected.WriteString(selectedMarkdown(markdown, frame.width, bounds, row, lastRow))
			row = lastRow
			continue
		}
		selected.WriteString(selectedLineText(lines[row], row, bounds, conversationPadding))
	}
	return selected.String()
}

func conversationSeparator(separators []string, row int) string {
	if row < len(separators) {
		return separators[row]
	}
	return "\n"
}

func conversationMarkdownAt(blocks []conversationMarkdown, row int) (conversationMarkdown, bool) {
	for _, block := range blocks {
		if row >= block.row && row < block.row+block.rows {
			return block, true
		}
	}
	return conversationMarkdown{}, false
}

func selectedMarkdown(markdown conversationMarkdown, width int, bounds selectionBounds, firstRow, lastRow int) string {
	startColumn := 0
	if firstRow == bounds.start.row {
		startColumn = bounds.start.column - conversationPadding
	}
	endColumn := width
	if lastRow == bounds.end.row {
		endColumn = bounds.end.column - conversationPadding
	}

	return text.MarkdownSelection(markdown.source, conversationContentWidth(width), firstRow-markdown.row, startColumn, lastRow-markdown.row, endColumn)
}

func selectedTextFromLines(lines []string, bounds selectionBounds) string {
	if len(lines) == 0 || bounds.start.row >= len(lines) || bounds.end.row < 0 {
		return ""
	}

	startRow := max(0, bounds.start.row)
	endRow := min(len(lines)-1, bounds.end.row)
	var selected strings.Builder
	for row := startRow; row <= endRow; row++ {
		if row > startRow {
			selected.WriteByte('\n')
		}
		selected.WriteString(selectedLineText(lines[row], row, bounds, 0))
	}
	return selected.String()
}

func selectedLineText(line string, row int, bounds selectionBounds, leftPadding int) string {
	columns := selectedColumns(line, row, bounds)
	selected := text.Slice(line, columns.start, columns.end)
	selected = strings.TrimRightFunc(selected, unicode.IsSpace)
	return trimLeftPadding(selected, leftPadding)
}

func trimLeftPadding(line string, padding int) string {
	for padding > 0 && strings.HasPrefix(line, " ") {
		line = line[1:]
		padding--
	}
	return line
}

func selectedColumns(line string, row int, bounds selectionBounds) cellRange {
	width := text.Width(line)
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
		width := text.RuneWidth(character)
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
		width := text.RuneWidth(character)
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
	if len(value) < 3 || value[0] != '\x1b' {
		return 0
	}
	switch value[1] {
	case '[':
		for index := 2; index < len(value); index++ {
			if value[index] >= 0x40 && value[index] <= 0x7e {
				return index + 1
			}
		}
	case ']':
		for index := 2; index < len(value); index++ {
			switch {
			case value[index] == '\a':
				return index + 1
			case value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\':
				return index + 2
			}
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
	if bounds.start.conversation {
		columns.start = max(columns.start, min(conversationPadding, text.Width(line)))
	}
	return columns, columns.end > columns.start
}
