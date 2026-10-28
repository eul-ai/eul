package terminal

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/filesearch"
	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/terminal/text"
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
	switch {
	case m.permission.active():
		return 0
	case m.resumePickerVisible():
		return max(1, min(pickerMaxVisible, len(m.resumePicker.items)))
	case m.commandPickerVisible():
		return min(pickerMaxVisible, len(m.commandPicker.items))
	case m.filePickerVisible():
		return pickerMaxVisible
	default:
		return 0
	}
}

func (m *tuiModel) subagentHeight() int {
	return min(len(m.subagentStatus.Active)+len(m.subagentStatus.PendingCompletions), max(0, m.height-5))
}

func modelInputLayout(model *tuiModel) (renderedInput, tuiLayout) {
	subagentHeight := model.subagentHeight()
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
	maximumHeight := maximumPermissionInputHeight(model.height - model.subagentHeight())
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

func wrappedPermissionDetail(permission permissionView, width int) []string {
	if permission.detail == "" {
		return nil
	}
	contentWidth := max(1, width-4-text.Width(permission.detailPrefix))
	return text.Wrap(permission.detail, contentWidth)
}

func renderPermission(model *tuiModel, width, maximumHeight int) renderedInput {
	height := min(maximumPermissionHeight, maximumHeight)
	permission := model.permission.view()
	panelStyle := lineStyle{
		foreground:      currentTheme.foreground,
		background:      currentTheme.editorLine,
		paintBackground: true,
	}
	mutedStyle := panelStyle
	mutedStyle.foreground = currentTheme.muted
	commandStyle := panelStyle
	commandStyle.foreground = currentTheme.markdownCode

	var descriptionSpans []text.Span
	if permission.subject != "" {
		descriptionSpans = append(descriptionSpans, text.Span{Text: permission.subject, Style: text.Style{Bold: true, Foreground: text.ForegroundAccent}})
	}
	if permission.subject != "" && permission.description != "" {
		descriptionSpans = append(descriptionSpans, text.Span{Text: " "})
	}
	if permission.description != "" {
		descriptionSpans = append(descriptionSpans, text.Span{Text: permission.description})
	}
	description := styledLine{spans: descriptionSpans, style: panelStyle, padding: 2}
	blank := styledLine{style: panelStyle}
	buttons := permissionButtons(model.permission.selected, panelStyle)

	detailLines := wrappedPermissionDetail(permission, width)
	capacity := permissionDetailCapacity(height)
	start := min(model.permission.scroll, max(0, len(detailLines)-capacity))
	end := min(len(detailLines), start+capacity)
	details := make([]styledLine, 0, end-start)
	for index, detail := range detailLines[start:end] {
		prefix := strings.Repeat(" ", text.Width(permission.detailPrefix))
		if index == 0 && start == 0 {
			prefix = permission.detailPrefix
		}
		details = append(details, styledLine{
			prefixText: prefix, prefixForeground: &currentTheme.accent,
			text: detail, style: commandStyle, padding: 2,
		})
	}
	notice := styledLine{text: permission.notice, style: mutedStyle, padding: 2}
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

func permissionButtons(selected session.PermissionDecision, style lineStyle) styledLine {
	spans := []text.Span{
		{Text: "› [d]eny once", Style: text.Style{Bold: true, Foreground: text.ForegroundError}},
		{Text: "     [a]llow once"},
		{Text: "     allow for [s]ession"},
	}
	switch selected {
	case session.PermissionAllowOnce:
		spans = []text.Span{
			{Text: "  [d]eny once"},
			{Text: "   › [a]llow once", Style: text.Style{Bold: true, Foreground: text.ForegroundSuccess}},
			{Text: "     allow for [s]ession"},
		}
	case session.PermissionAllowSession:
		spans = []text.Span{
			{Text: "  [d]eny once"},
			{Text: "     [a]llow once"},
			{Text: "   › allow for [s]ession", Style: text.Style{Bold: true, Foreground: text.ForegroundSuccess}},
		}
	}
	style.foreground = currentTheme.muted
	return styledLine{spans: spans, style: style}
}

func isEditorWord(item editorItem) bool {
	return item.kind == editorItemRune && !unicode.IsSpace(item.character)
}

func renderInput(model *tuiModel, width, maximumHeight int) renderedInput {
	if width < 1 || maximumHeight < 1 {
		return renderedInput{}
	}
	if model.permission.active() {
		return renderPermission(model, width, maximumHeight)
	}
	firstPrefix := "> "
	continuationPrefix := "  "
	prefixWidth := 2
	switch width {
	case 1:
		firstPrefix = ""
		continuationPrefix = ""
		prefixWidth = 0
	case 2:
		firstPrefix = ">"
		continuationPrefix = " "
		prefixWidth = 1
	}

	contentWidth := width - prefixWidth
	contents := make([]string, 0, 2)
	var line strings.Builder
	lineWidth := 0
	cursorRow := 0
	cursorColumn := prefixWidth + 1
	cursorFound := false
	flush := func() {
		contents = append(contents, line.String())
		line.Reset()
		lineWidth = 0
	}
	setCursor := func(index int) {
		if index != model.cursor || cursorFound {
			return
		}
		cursorRow = len(contents)
		cursorColumn = prefixWidth + lineWidth + 1
		cursorFound = true
	}

	for index, item := range model.input {
		setCursor(index)
		if isEditorNewline(item) {
			flush()
			continue
		}

		if item.kind != editorItemRune {
			label := imageAttachmentLabel
			if item.kind == editorItemPendingImage {
				label = "[loading image]"
			}
			labelWidth := text.Width(label)
			if lineWidth > 0 && lineWidth+labelWidth > contentWidth {
				cursorAtItem := index == model.cursor
				flush()
				if cursorAtItem {
					cursorRow = len(contents)
					cursorColumn = prefixWidth + 1
				}
			}
			displayWidth := min(labelWidth, contentWidth)
			line.WriteString(text.Truncate(label, displayWidth, false))
			lineWidth += displayWidth
			if lineWidth == contentWidth {
				flush()
			}
			continue
		}

		character := item.character
		if !unicode.IsSpace(character) && (index == 0 || !isEditorWord(model.input[index-1])) {
			wordWidth := 0
			for wordEnd := index; wordEnd < len(model.input) && isEditorWord(model.input[wordEnd]); wordEnd++ {
				wordWidth += text.RuneWidth(model.input[wordEnd].character)
			}
			if lineWidth > 0 && lineWidth+wordWidth > contentWidth {
				cursorAtItem := index == model.cursor
				flush()
				if cursorAtItem {
					cursorRow = len(contents)
					cursorColumn = prefixWidth + 1
				}
			}
		}

		characterWidth := text.RuneWidth(character)
		if lineWidth > 0 && lineWidth+characterWidth > contentWidth {
			cursorAtItem := index == model.cursor
			flush()
			if cursorAtItem {
				cursorRow = len(contents)
				cursorColumn = prefixWidth + 1
			}
		}
		line.WriteRune(character)
		lineWidth += characterWidth
	}
	if model.cursor == len(model.input) && lineWidth >= contentWidth {
		flush()
	}
	setCursor(len(model.input))
	flush()

	lines := make([]string, len(contents))
	for index, content := range contents {
		prefix := continuationPrefix
		if index == 0 {
			prefix = firstPrefix
		}
		lines[index] = text.Truncate(prefix+content, width, false)
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
	if len(model.resumePicker.items) == 0 {
		return []styledLine{{text: "  no saved sessions", style: lineStyle{foreground: currentTheme.muted}}}
	}

	return renderPickerRows(model.resumePicker.pickerList, height, func(summary session.Summary, _ int) styledLine {
		detail := summary.ID + " · " + summary.UpdatedAt.Local().Format("Jan 2 15:04")
		if summary.Active {
			detail += " · interrupted"
		}
		return styledLine{
			text:      resumeSummaryLabel(summary),
			rightText: detail,
			style:     lineStyle{foreground: currentTheme.muted},
		}
	})
}

func renderCommandPicker(model *tuiModel, height int) []styledLine {
	if height <= 0 {
		return nil
	}

	return renderPickerRows(model.commandPicker.pickerList, height, func(match commandCompletion, _ int) styledLine {
		description := ""
		if model.width >= 30 {
			description = text.Truncate(match.description, model.width/2, true)
		}
		return styledLine{text: match.text, rightText: description, style: lineStyle{foreground: currentTheme.muted}}
	})
}

func renderFilePicker(model *tuiModel, height int) []styledLine {
	if height <= 0 {
		return nil
	}
	if len(model.filePicker.items) == 0 {
		text := "  no matching files"
		switch {
		case !model.filePicker.matchesCurrent || model.filePicker.state == filesearch.StateDiscovering:
			text = "  searching files…"
		case model.filePicker.state == filesearch.StateLimited:
			text = "  no matching files · search limited"
		case model.filePicker.state == filesearch.StateFailed:
			text = "  file search failed"
			if model.filePicker.err != "" {
				text += ": " + singleLine(model.filePicker.err, 120)
			}
		}
		return []styledLine{{text: text, style: lineStyle{foreground: currentTheme.muted}}}
	}

	status := filePickerStatus(model)
	return renderPickerRows(model.filePicker.pickerList, height, func(match filesearch.Match, index int) styledLine {
		parent := filePickerParent(match.Display)
		if index == 0 && status != "" {
			if parent != "" {
				parent += " · "
			}
			parent += status
		}
		return styledLine{
			spans:           filePickerNameSpans(match),
			rightText:       text.Truncate(parent, max(0, model.width/2), true),
			rightForeground: &currentTheme.muted,
			style:           lineStyle{foreground: currentTheme.foreground},
		}
	})
}

func filePickerStatus(model *tuiModel) string {
	if !model.filePicker.matchesCurrent {
		return "updating…"
	}
	switch model.filePicker.state {
	case filesearch.StateDiscovering:
		return "searching…"
	case filesearch.StateLimited:
		return "search limited"
	case filesearch.StateFailed:
		return "refresh failed"
	default:
		return ""
	}
}

func filePickerParent(display string) string {
	trimmed := strings.TrimSuffix(display, "/")
	index := strings.LastIndex(trimmed, "/")
	if index < 0 {
		return ""
	}
	if index == 0 {
		return "/"
	}
	return trimmed[:index]
}

func filePickerNameSpans(match filesearch.Match) []text.Span {
	name := match.Name
	if match.IsDir {
		name += "/"
	}
	trimmed := strings.TrimSuffix(match.Display, "/")
	baseOffset := utf8.RuneCountInString(trimmed) - utf8.RuneCountInString(match.Name)
	matched := make(map[int]struct{}, len(match.MatchPositions))
	for _, position := range match.MatchPositions {
		if position >= baseOffset {
			matched[position-baseOffset] = struct{}{}
		}
	}

	spans := make([]text.Span, 0, len([]rune(name)))
	for index, character := range []rune(name) {
		style := text.Style{}
		if _, ok := matched[index]; ok {
			style = text.Style{Bold: true, Foreground: text.ForegroundAccent}
		}
		spans = text.AppendSpan(spans, string(character), style)
	}
	return spans
}
