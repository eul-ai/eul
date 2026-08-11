package terminal

import (
	"fmt"
	"strings"
	"unicode"
)

type tuiLayout struct {
	conversationHeight int
	subagentRow        int
	subagentHeight     int
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

func calculateLayout(height, inputHeight, pickerHeight, subagentHeight int) tuiLayout {
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
	subagentHeight = max(0, min(subagentHeight, height-pickerHeight-5))
	inputHeight = max(1, min(inputHeight, height-pickerHeight-subagentHeight-3))
	conversationHeight := height - inputHeight - pickerHeight - subagentHeight - 3
	subagentRow := 0
	if subagentHeight > 0 {
		subagentRow = conversationHeight + 1
	}
	topRuleRow := conversationHeight + subagentHeight + 1
	bottomRuleRow := topRuleRow + inputHeight + 1
	pickerRow := 0
	if pickerHeight > 0 {
		pickerRow = bottomRuleRow + 1
	}
	return tuiLayout{
		conversationHeight: conversationHeight,
		subagentRow:        subagentRow,
		subagentHeight:     subagentHeight,
		topRuleRow:         topRuleRow,
		inputRow:           topRuleRow + 1,
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

func maximumPickerHeight(height int) int {
	return max(0, height-5)
}

func (m *tuiModel) pickerHeight() int {
	if m.resumePickerVisible() {
		return m.resumePickerHeight()
	}
	if m.commandPickerVisible() {
		return m.commandPickerHeight()
	}
	return m.filePickerHeight()
}

func modelInputLayout(model *tuiModel) (renderedInput, tuiLayout) {
	subagentHeight := min(len(model.subagentStatus.Jobs), max(0, model.height-5))
	pickerHeight := min(model.pickerHeight(), max(0, maximumPickerHeight(model.height)-subagentHeight))
	input := renderInput(model, model.width, maximumInputHeight(model.height-subagentHeight, pickerHeight))
	return input, calculateLayout(model.height, len(input.lines), pickerHeight, subagentHeight)
}

func renderInput(model *tuiModel, width, maximumHeight int) renderedInput {
	if width < 1 || maximumHeight < 1 {
		return renderedInput{}
	}
	if width <= 2 {
		return renderedInput{lines: []string{truncateCells("> ", width, false)}, cursorColumn: width}
	}

	contentWidth := width - 2
	contents := make([]string, 0, 2)
	if len(model.images) == 1 {
		contents = append(contents, "[image attached]")
	} else if len(model.images) > 1 {
		contents = append(contents, fmt.Sprintf("[%d images attached]", len(model.images)))
	}
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

		if !unicode.IsSpace(character) && (index == 0 || unicode.IsSpace(model.input[index-1])) {
			wordWidth := 0
			for wordEnd := index; wordEnd < len(model.input) && !unicode.IsSpace(model.input[wordEnd]); wordEnd++ {
				wordWidth += runeWidth(model.input[wordEnd])
			}
			if lineWidth > 0 && lineWidth+wordWidth > contentWidth {
				flush()
			}
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

func renderPicker(model *tuiModel, height int) []styledLine {
	if model.resumePickerVisible() {
		return renderResumePicker(model, height)
	}
	if model.commandPickerVisible() {
		return renderCommandPicker(model, height)
	}
	return renderFilePicker(model, height)
}

func renderResumePicker(model *tuiModel, height int) []styledLine {
	if height <= 0 {
		return nil
	}
	if len(model.resumePicker.matches) == 0 {
		return []styledLine{{text: "  no saved sessions", style: lineStyle{foreground: currentTheme.muted}}}
	}

	selectedID := ""
	if model.resumePicker.selected >= 0 && model.resumePicker.selected < len(model.resumePicker.matches) {
		selectedID = model.resumePicker.matches[model.resumePicker.selected].ID
	}
	matches := model.visibleResumePickerMatches()
	lines := make([]styledLine, 0, min(height, len(matches)))
	for _, match := range matches[:min(height, len(matches))] {
		detail := match.ID + " · " + match.UpdatedAt.Local().Format("Jan 2 15:04")
		if match.Active {
			detail += " · interrupted"
		}
		line := styledLine{
			prefixText: "  ",
			text:       resumeSummaryLabel(match),
			rightText:  detail,
			style:      lineStyle{foreground: currentTheme.muted},
		}
		if match.ID == selectedID {
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

func renderCommandPicker(model *tuiModel, height int) []styledLine {
	if height <= 0 {
		return nil
	}

	selectedText := ""
	if model.commandPicker.selected >= 0 && model.commandPicker.selected < len(model.commandPicker.matches) {
		selectedText = model.commandPicker.matches[model.commandPicker.selected].text
	}
	matches := model.visibleCommandPickerMatches()
	lines := make([]styledLine, 0, min(height, len(matches)))
	for _, match := range matches[:min(height, len(matches))] {
		description := ""
		if model.width >= 30 {
			description = truncateCells(match.description, model.width/2, true)
		}
		line := styledLine{
			prefixText: "  ",
			text:       match.text,
			rightText:  description,
			style:      lineStyle{foreground: currentTheme.muted},
		}
		if match.text == selectedText {
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
