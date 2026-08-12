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
	styledLines  []styledLine
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
	if m.permission.active() {
		return 0
	}
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
	availableHeight := model.height - subagentHeight
	maximumHeight := maximumInputHeight(availableHeight, pickerHeight)
	if model.permission.active() {
		maximumHeight = maximumPermissionInputHeight(availableHeight)
	}
	input := renderInput(model, model.width, maximumHeight)
	return input, calculateLayout(model.height, len(input.lines), pickerHeight, subagentHeight)
}

const maximumPermissionHeight = 12

func maximumPermissionInputHeight(height int) int {
	maximumHeight := maximumInputHeight(height, 0)
	if height >= 5 {
		maximumHeight = min(maximumHeight+1, height-3)
	}
	return maximumHeight
}

func permissionDetailCapacityForModel(model *tuiModel) int {
	subagentHeight := min(len(model.subagentStatus.Jobs), max(0, model.height-5))
	maximumHeight := maximumPermissionInputHeight(model.height - subagentHeight)
	return permissionDetailCapacity(min(maximumPermissionHeight, maximumHeight))
}

func permissionDetailCapacity(height int) int {
	switch {
	case height <= 1:
		return 0
	case height <= 3:
		return 1
	case height <= 5:
		return 0
	case height <= 7:
		return height - 6
	default:
		return height - 8
	}
}

func wrappedPermissionDetail(permission permissionModel, width int) []string {
	if permission.detail == "" {
		return nil
	}
	contentWidth := max(1, width-4-cellWidth(permission.detailPrefix))
	return wrapText(permission.detail, contentWidth)
}

func renderPermission(model *tuiModel, width, maximumHeight int) renderedInput {
	height := min(maximumPermissionHeight, maximumHeight)
	panelStyle := lineStyle{
		foreground:      currentTheme.foreground,
		background:      currentTheme.editorLine,
		paintBackground: true,
	}
	mutedStyle := panelStyle
	mutedStyle.foreground = currentTheme.muted
	commandStyle := panelStyle
	commandStyle.foreground = currentTheme.markdownCode
	commandStyle.background = currentTheme.toolPendingBackground

	var descriptionSpans []inlineSpan
	if model.permission.subject != "" {
		descriptionSpans = append(descriptionSpans, inlineSpan{text: model.permission.subject, style: inlineStyle{bold: true, foreground: inlineForegroundAccent}})
	}
	if model.permission.subject != "" && model.permission.description != "" {
		descriptionSpans = append(descriptionSpans, inlineSpan{text: " "})
	}
	if model.permission.description != "" {
		descriptionSpans = append(descriptionSpans, inlineSpan{text: model.permission.description})
	}
	description := styledLine{spans: descriptionSpans, style: panelStyle, padding: 2}
	blank := styledLine{style: panelStyle}
	buttons := permissionButtons(model.permission.allowSelected, panelStyle)

	detailLines := wrappedPermissionDetail(model.permission, width)
	capacity := permissionDetailCapacity(height)
	start := min(model.permission.scroll, max(0, len(detailLines)-capacity))
	end := min(len(detailLines), start+capacity)
	details := make([]styledLine, 0, end-start)
	for index, detail := range detailLines[start:end] {
		prefix := strings.Repeat(" ", cellWidth(model.permission.detailPrefix))
		if index == 0 && start == 0 {
			prefix = model.permission.detailPrefix
		}
		details = append(details, styledLine{
			prefixText: prefix, prefixForeground: &currentTheme.accent,
			text: detail, style: commandStyle, padding: 2,
		})
	}
	notice := styledLine{text: model.permission.notice, style: mutedStyle, padding: 2}
	if len(detailLines) > capacity && capacity > 0 {
		notice.rightText = fmt.Sprintf("lines %d–%d of %d", start+1, end, len(detailLines))
	}

	var styled []styledLine
	switch {
	case height == 1:
		styled = []styledLine{buttons}
	case height == 2:
		styled = append(details, buttons)
	case height == 3:
		styled = append([]styledLine{description}, details...)
		styled = append(styled, buttons)
	case height == 4:
		styled = append(styled, notice, blank, buttons, blank)
	case height == 5:
		styled = append(styled, blank, notice, blank, buttons, blank)
	case height <= 7:
		styled = append(styled, description)
		styled = append(styled, details...)
		styled = append(styled, blank, notice, blank, buttons, blank)
	default:
		styled = append(styled, blank, description, blank)
		styled = append(styled, details...)
		styled = append(styled, blank, notice, blank, buttons, blank)
	}
	styled = styled[:min(len(styled), height)]

	lines := make([]string, len(styled))
	for index, line := range styled {
		lines[index] = renderedLineText(line, width)
	}
	return renderedInput{lines: lines, styledLines: styled}
}

func permissionButtons(allowSelected bool, style lineStyle) styledLine {
	spans := []inlineSpan{
		{text: "› [n] Deny", style: inlineStyle{bold: true, foreground: inlineForegroundError}},
		{text: "     [y] Allow once"},
	}
	if allowSelected {
		spans = []inlineSpan{
			{text: "  [n] Deny"},
			{text: "   › [y] Allow once", style: inlineStyle{bold: true, foreground: inlineForegroundSuccess}},
		}
	}
	style.foreground = currentTheme.muted
	return styledLine{spans: spans, style: style}
}

func renderInput(model *tuiModel, width, maximumHeight int) renderedInput {
	if width < 1 || maximumHeight < 1 {
		return renderedInput{}
	}
	if model.permission.active() {
		return renderPermission(model, width, maximumHeight)
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
