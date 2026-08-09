package terminal

import "strings"

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
