package terminal

import (
	"bytes"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

func editorItemsFromText(text string) []editorItem {
	items := make([]editorItem, 0, utf8.RuneCountInString(text))
	for _, character := range text {
		items = append(items, editorItem{kind: editorItemRune, character: character})
	}
	return items
}

func editorText(items []editorItem) string {
	var text strings.Builder
	for _, item := range items {
		if item.kind == editorItemRune {
			text.WriteRune(item.character)
		}
	}
	return text.String()
}

func (m *tuiModel) inputText() string {
	return editorText(m.input)
}

func (m *tuiModel) textCursor() int {
	cursor := 0
	for _, item := range m.input[:m.cursor] {
		if item.kind == editorItemRune {
			cursor++
		}
	}
	return cursor
}

func (m *tuiModel) inputReferenceRunes() []rune {
	input := make([]rune, len(m.input))
	for index, item := range m.input {
		if item.kind == editorItemRune {
			input[index] = item.character
		} else {
			input[index] = ' '
		}
	}
	return input
}

func (m *tuiModel) replaceItemRange(start, end int, replacement string) bool {
	if start < 0 || start > end || end > len(m.input) {
		return false
	}

	inserted := editorItemsFromText(replacement)
	updated := make([]editorItem, 0, len(m.input)-(end-start)+len(inserted))
	updated = append(updated, m.input[:start]...)
	updated = append(updated, inserted...)
	updated = append(updated, m.input[end:]...)
	m.input = updated
	m.cursor = start + len(inserted)
	return true
}

func (m *tuiModel) imageCount() int {
	count := 0
	for _, item := range m.input {
		if item.kind == editorItemImage {
			count++
		}
	}
	return count
}

func (m *tuiModel) imageBytes() int {
	total := 0
	for _, item := range m.input {
		if item.kind == editorItemImage && item.image != nil {
			total += len(item.image.Data)
		}
	}
	return total
}

func (m *tuiModel) submittingContent() ([]agent.ContentPart, bool) {
	content := editorContent(m.input)
	if len(content) == 0 || len(content) == 1 && content[0].Kind == agent.ContentPartText && strings.TrimSpace(content[0].Text) == "" {
		return nil, false
	}
	return content, true
}

func (m *tuiModel) finishSubmission(content []agent.ContentPart) {
	m.rememberPrompt(contentText(content))
	m.clearInput()
}

func (m *tuiModel) insertInput(text string) error {
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return errInvalidInput
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Map(func(character rune) rune {
		switch character {
		case '\n':
			return '\n'
		case '\t':
			return ' '
		}
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, text)
	if len(m.inputText())+len(text) > maxInputBytes {
		return errInputTooLong
	}

	m.insertRunes([]rune(text))
	m.refreshInputPickers(true)
	return nil
}

func (m *tuiModel) insertNewline() error {
	if len(m.inputText())+1 > maxInputBytes {
		return errInputTooLong
	}
	m.insertRunes([]rune{'\n'})
	m.clearInputPickers()
	return nil
}

func (m *tuiModel) insertRunes(inserted []rune) {
	m.insertItems(editorItemsFromText(string(inserted)))
}

func (m *tuiModel) insertItems(inserted []editorItem) {
	m.leaveHistory()
	m.input = append(m.input, inserted...)
	copy(m.input[m.cursor+len(inserted):], m.input[m.cursor:len(m.input)-len(inserted)])
	copy(m.input[m.cursor:], inserted)
	m.cursor += len(inserted)
}

func (m *tuiModel) clearInput() []uint64 {
	pending := m.pendingImageRequests()
	m.input = nil
	m.cursor = 0
	m.historyIndex = -1
	m.historyDraft = nil
	m.historyDraftCursor = 0
	m.clearInputPickers()
	return pending
}

func (m *tuiModel) pendingImageRequests() []uint64 {
	var requests []uint64
	for _, item := range m.input {
		if item.kind == editorItemPendingImage {
			requests = append(requests, item.requestID)
		}
	}
	return requests
}

func (m *tuiModel) refreshInputPickers(reopen bool) {
	m.refreshCommandPicker(reopen)
	m.refreshFilePicker(reopen)
}

func (m *tuiModel) clearInputPickers() {
	m.clearCommandPicker()
	m.clearFilePicker()
	m.dismissResumePicker()
}

func (m *tuiModel) nextThinkingLevel() (agent.ThinkingLevel, error) {
	if !m.thinkingSelectionAvailable || len(m.thinkingLevels) == 0 {
		return "", errors.New("thinking level selection is unavailable")
	}

	next := m.thinkingLevels[0]
	for index, level := range m.thinkingLevels {
		if level == m.thinkingLevel {
			next = m.thinkingLevels[(index+1)%len(m.thinkingLevels)]
			break
		}
	}
	return next, nil
}

func (m *tuiModel) backspace() uint64 {
	if m.cursor == 0 {
		return 0
	}

	m.leaveHistory()
	requestID := m.input[m.cursor-1].requestID
	copy(m.input[m.cursor-1:], m.input[m.cursor:])
	m.input = m.input[:len(m.input)-1]
	m.cursor--
	m.refreshInputPickers(true)
	return requestID
}

func (m *tuiModel) delete() uint64 {
	if m.cursor >= len(m.input) {
		return 0
	}

	m.leaveHistory()
	requestID := m.input[m.cursor].requestID
	copy(m.input[m.cursor:], m.input[m.cursor+1:])
	m.input = m.input[:len(m.input)-1]
	m.refreshInputPickers(true)
	return requestID
}

func (m *tuiModel) moveLeft() {
	if m.cursor > 0 {
		m.cursor--
		m.refreshInputPickers(false)
	}
}

func (m *tuiModel) moveRight() {
	if m.cursor < len(m.input) {
		m.cursor++
		m.refreshInputPickers(false)
	}
}

func isEditorNewline(item editorItem) bool {
	return item.kind == editorItemRune && item.character == '\n'
}

func (m *tuiModel) moveHome() {
	for m.cursor > 0 && !isEditorNewline(m.input[m.cursor-1]) {
		m.cursor--
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveEnd() {
	for m.cursor < len(m.input) && !isEditorNewline(m.input[m.cursor]) {
		m.cursor++
	}
	m.refreshInputPickers(false)
}

func (m *tuiModel) moveUp() bool {
	lineStart := m.cursor
	for lineStart > 0 && !isEditorNewline(m.input[lineStart-1]) {
		lineStart--
	}
	if lineStart == 0 {
		return false
	}

	previousLineEnd := lineStart - 1
	previousLineStart := previousLineEnd
	for previousLineStart > 0 && !isEditorNewline(m.input[previousLineStart-1]) {
		previousLineStart--
	}

	column := m.cursor - lineStart
	m.cursor = min(previousLineStart+column, previousLineEnd)
	m.refreshInputPickers(false)
	return true
}

func (m *tuiModel) moveDown() bool {
	lineStart := m.cursor
	for lineStart > 0 && !isEditorNewline(m.input[lineStart-1]) {
		lineStart--
	}

	lineEnd := m.cursor
	for lineEnd < len(m.input) && !isEditorNewline(m.input[lineEnd]) {
		lineEnd++
	}
	if lineEnd == len(m.input) {
		return false
	}

	nextLineStart := lineEnd + 1
	nextLineEnd := nextLineStart
	for nextLineEnd < len(m.input) && !isEditorNewline(m.input[nextLineEnd]) {
		nextLineEnd++
	}

	column := m.cursor - lineStart
	m.cursor = min(nextLineStart+column, nextLineEnd)
	m.refreshInputPickers(false)
	return true
}

func (m *tuiModel) historyUp() {
	if len(m.history) == 0 {
		return
	}

	if m.historyIndex < 0 {
		m.historyDraft = make([]editorItem, 0, len(m.input))
		m.historyDraftCursor = m.cursor
		for index, item := range m.input {
			if item.kind == editorItemPendingImage {
				if index < m.cursor {
					m.historyDraftCursor--
				}
				continue
			}
			m.historyDraft = append(m.historyDraft, item)
		}
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.setInput(m.history[m.historyIndex])
}

func (m *tuiModel) historyDown() {
	if m.historyIndex < 0 {
		return
	}

	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.setInput(m.history[m.historyIndex])
		return
	}

	m.historyIndex = -1
	m.input = m.historyDraft
	m.cursor = m.historyDraftCursor
	m.historyDraft = nil
	m.historyDraftCursor = 0
	m.clearInputPickers()
}

func (m *tuiModel) leaveHistory() {
	if m.historyIndex < 0 {
		return
	}
	m.historyIndex = -1
	m.historyDraft = nil
	m.historyDraftCursor = 0
}

func (m *tuiModel) setInput(value string) {
	m.input = editorItemsFromText(value)
	m.cursor = len(m.input)
	m.clearInputPickers()
}

func contentText(content []agent.ContentPart) string {
	var text strings.Builder
	for _, part := range content {
		if part.Kind == agent.ContentPartText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func cloneTerminalContent(content []agent.ContentPart) []agent.ContentPart {
	cloned := make([]agent.ContentPart, len(content))
	for index, part := range content {
		cloned[index] = part
		if part.Image != nil {
			image := *part.Image
			image.Data = append([]byte(nil), image.Data...)
			cloned[index].Image = &image
		}
	}
	return cloned
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

func sanitizeContent(content []agent.ContentPart) []agent.ContentPart {
	content = cloneTerminalContent(content)
	for index := range content {
		if content[index].Kind == agent.ContentPartText {
			content[index].Text = sanitizeAssistantText(content[index].Text)
		}
	}
	return content
}

func editorItemsFromContent(content []agent.ContentPart) []editorItem {
	var items []editorItem
	for _, part := range content {
		switch part.Kind {
		case agent.ContentPartText:
			items = append(items, editorItemsFromText(part.Text)...)
		case agent.ContentPartImage:
			if part.Image == nil {
				continue
			}
			image := *part.Image
			image.Data = append([]byte(nil), image.Data...)
			items = append(items, editorItem{kind: editorItemImage, image: &image})
		}
	}
	return items
}

func editorContent(items []editorItem) []agent.ContentPart {
	var content []agent.ContentPart
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		content = append(content, agent.ContentPart{Kind: agent.ContentPartText, Text: text.String()})
		text.Reset()
	}

	for _, item := range items {
		switch item.kind {
		case editorItemRune:
			text.WriteRune(item.character)
		case editorItemImage:
			if item.image == nil {
				continue
			}
			flushText()
			image := *item.image
			image.Data = append([]byte(nil), image.Data...)
			content = append(content, agent.ContentPart{Kind: agent.ContentPartImage, Image: &image})
		}
	}
	flushText()
	return content
}

func (m *tuiModel) takePrompt() (string, bool) {
	prompt := m.inputText()
	if strings.TrimSpace(prompt) == "" {
		return "", false
	}
	m.rememberPrompt(prompt)
	m.clearInput()
	return prompt, true
}

func (m *tuiModel) rememberPrompt(prompt string) {
	if strings.TrimSpace(prompt) != "" && (len(m.history) == 0 || m.history[len(m.history)-1] != prompt) {
		m.history = append(m.history, prompt)
	}
}

func (m *tuiModel) reserveImage(requestID uint64) {
	m.insertItems([]editorItem{{kind: editorItemPendingImage, requestID: requestID}})
	m.clearInputPickers()
}

func (m *tuiModel) resolveImage(requestID uint64, image agent.Image) error {
	index := -1
	for itemIndex, item := range m.input {
		if item.kind == editorItemPendingImage && item.requestID == requestID {
			index = itemIndex
			break
		}
	}
	if index < 0 {
		return nil
	}
	if m.imageCount() >= maxAttachedImages {
		return errTooManyImages
	}
	if m.imageBytes()+len(image.Data) > maxAttachedImagesTotalBytes {
		return errImagesTooLarge
	}

	image.Data = append([]byte(nil), image.Data...)
	m.input[index] = editorItem{kind: editorItemImage, image: &image}
	m.clearInputPickers()
	if !m.running {
		m.activity = activity{kind: activityReady, detail: "image attached"}
	}
	return nil
}

func (m *tuiModel) removePendingImage(requestID uint64) bool {
	for index := range m.input {
		if m.input[index].kind != editorItemPendingImage || m.input[index].requestID != requestID {
			continue
		}
		copy(m.input[index:], m.input[index+1:])
		m.input = m.input[:len(m.input)-1]
		if m.cursor > index {
			m.cursor--
		}
		return true
	}
	return false
}

func (m *tuiModel) attachImage(image agent.Image) error {
	const immediateRequestID = ^uint64(0)
	m.reserveImage(immediateRequestID)
	if err := m.resolveImage(immediateRequestID, image); err != nil {
		m.removePendingImage(immediateRequestID)
		return err
	}
	return nil
}
