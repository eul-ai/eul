package terminal

import (
	"errors"
	"os"
	"strings"
	"unicode"
)

const filePickerMaxVisible = 5

type filePickerState struct {
	matches        []fileSearchMatch
	matchesCurrent bool
	query          string
	tokenStart     int
	tokenEnd       int
	selected       int
	requestID      uint64
	pending        *fileSearchRequest
	state          fileSearchState
	err            string
	enabled        bool
	active         bool
	dismissed      bool
	cancelSearch   bool
}

func fileReferenceToken(input []rune, cursor int) (int, int, string, bool) {
	if cursor < 0 || cursor > len(input) {
		return 0, 0, "", false
	}

	for start := 0; start < len(input); {
		for start < len(input) && unicode.IsSpace(input[start]) {
			start++
		}
		if start >= len(input) {
			break
		}

		end := start
		quoted := false
		escaped := false
		for end < len(input) {
			character := input[end]
			switch {
			case escaped:
				escaped = false
			case quoted && character == '\\':
				escaped = true
			case character == '"':
				quoted = !quoted
			case !quoted && unicode.IsSpace(character):
				goto tokenComplete
			}
			end++
		}

	tokenComplete:
		if cursor > start && cursor <= end {
			if input[start] != '@' {
				return 0, 0, "", false
			}
			query, ok := decodeFileReferenceQuery(string(input[start+1 : end]))
			if !ok {
				return 0, 0, "", false
			}
			return start, end, query, true
		}
		start = end + 1
	}
	return 0, 0, "", false
}

func decodeFileReferenceQuery(value string) (string, bool) {
	if !strings.HasPrefix(value, `"`) {
		return value, true
	}

	value = strings.TrimPrefix(value, `"`)
	if hasUnescapedClosingQuote(value) {
		value = strings.TrimSuffix(value, `"`)
	}
	var decoded strings.Builder
	escaped := false
	for _, character := range value {
		switch {
		case escaped && (character == '\\' || character == '"'):
			decoded.WriteRune(character)
			escaped = false
		case escaped:
			decoded.WriteRune('\\')
			decoded.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		default:
			decoded.WriteRune(character)
		}
	}
	if escaped {
		decoded.WriteRune('\\')
	}
	return decoded.String(), true
}

func hasUnescapedClosingQuote(value string) bool {
	if !strings.HasSuffix(value, `"`) {
		return false
	}
	backslashes := 0
	for index := len(value) - 2; index >= 0 && value[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func (m *tuiModel) refreshFilePicker(reopen bool) {
	if !m.filePicker.enabled {
		m.clearFilePicker()
		return
	}
	start, end, query, ok := fileReferenceToken(m.inputReferenceRunes(), m.cursor)
	if !ok {
		m.clearFilePicker()
		return
	}
	if reopen {
		m.filePicker.dismissed = false
	}
	if m.filePicker.dismissed || !m.filePicker.active && !reopen {
		return
	}

	wasActive := m.filePicker.active
	previousQuery := m.filePicker.query
	m.filePicker.active = true
	m.filePicker.tokenStart = start
	m.filePicker.tokenEnd = end
	if wasActive && query == previousQuery {
		return
	}

	m.filePicker.query = query
	if !wasActive {
		m.filePicker.matches = nil
		m.filePicker.selected = 0
		m.filePicker.state = fileSearchDiscovering
		m.filePicker.err = ""
	}
	m.filePicker.matchesCurrent = false
	m.filePicker.requestID++
	request := fileSearchRequest{id: m.filePicker.requestID, query: query, refresh: !wasActive}
	m.filePicker.pending = &request
}

func (m *tuiModel) takeFileSearchCommand() fileSearchCommand {
	command := fileSearchCommand{cancel: m.filePicker.cancelSearch}
	m.filePicker.cancelSearch = false
	if m.filePicker.pending != nil {
		request := *m.filePicker.pending
		command.request = &request
		m.filePicker.pending = nil
	}
	return command
}

func (m *tuiModel) applyFileSearchResult(result fileSearchResult) bool {
	if result.id != m.filePicker.requestID || !m.filePicker.active {
		return false
	}
	selectedPath := ""
	if m.filePicker.selected >= 0 && m.filePicker.selected < len(m.filePicker.matches) {
		selectedPath = m.filePicker.matches[m.filePicker.selected].identity()
	}
	m.filePicker.state = result.state
	m.filePicker.err = result.err
	m.filePicker.matches = append([]fileSearchMatch(nil), result.matches...)
	m.filePicker.matchesCurrent = true
	m.filePicker.selected = 0
	if selectedPath != "" {
		for index, match := range m.filePicker.matches {
			if match.identity() == selectedPath {
				m.filePicker.selected = index
				break
			}
		}
	}
	return true
}

func (m *tuiModel) clearFilePicker() {
	enabled := m.filePicker.enabled
	cancelSearch := m.filePicker.active || m.filePicker.pending != nil
	requestID := m.filePicker.requestID + 1
	m.filePicker = filePickerState{
		enabled:      enabled,
		requestID:    requestID,
		cancelSearch: cancelSearch,
	}
}

func (m *tuiModel) dismissFilePicker() {
	m.filePicker.active = false
	m.filePicker.matches = nil
	m.filePicker.pending = nil
	m.filePicker.dismissed = true
	m.filePicker.requestID++
	m.filePicker.cancelSearch = true
}

func (m *tuiModel) filePickerVisible() bool {
	return maximumPickerHeight(m.height) > 0 && m.filePicker.active
}

func (m *tuiModel) filePickerHeight() int {
	if !m.filePickerVisible() {
		return 0
	}
	return filePickerMaxVisible
}

func (m *tuiModel) moveFilePickerSelection(direction int) {
	count := len(m.filePicker.matches)
	if count == 0 {
		return
	}
	m.filePicker.selected = (m.filePicker.selected + direction + count) % count
}

func (m *tuiModel) applyFilePickerSelection() error {
	picker := &m.filePicker
	if !picker.matchesCurrent {
		return nil
	}
	if picker.selected < 0 || picker.selected >= len(picker.matches) {
		return nil
	}
	match := picker.matches[picker.selected]
	if err := validateFileSearchMatch(match); err != nil {
		picker.matches = append(picker.matches[:picker.selected], picker.matches[picker.selected+1:]...)
		picker.selected = min(picker.selected, max(0, len(picker.matches)-1))
		return err
	}

	reference := formatFileReference(match.reference)
	if picker.tokenEnd >= len(m.input) || m.input[picker.tokenEnd].kind != editorItemRune || !unicode.IsSpace(m.input[picker.tokenEnd].character) {
		reference += " "
	}
	removedBytes := len(editorText(m.input[picker.tokenStart:picker.tokenEnd]))
	if len(m.inputText())-removedBytes+len(reference) > maxInputBytes {
		return errInputTooLong
	}

	m.leaveHistory()
	if !m.replaceItemRange(picker.tokenStart, picker.tokenEnd, reference) {
		return nil
	}
	m.clearFilePicker()
	return nil
}

func validateFileSearchMatch(match fileSearchMatch) error {
	if match.path == "" {
		return nil
	}
	if _, err := os.Lstat(match.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("selected path no longer exists")
		}
		return err
	}
	return nil
}

func (m *tuiModel) drillIntoFilePickerDirectory() error {
	picker := &m.filePicker
	if !picker.matchesCurrent {
		return nil
	}
	if picker.selected < 0 || picker.selected >= len(picker.matches) || !picker.matches[picker.selected].directory {
		return nil
	}
	match := picker.matches[picker.selected]
	if err := validateFileSearchMatch(match); err != nil {
		return err
	}

	reference := formatFileReference(match.navigation)
	removedBytes := len(editorText(m.input[picker.tokenStart:picker.tokenEnd]))
	if len(m.inputText())-removedBytes+len(reference) > maxInputBytes {
		return errInputTooLong
	}

	m.leaveHistory()
	if !m.replaceItemRange(picker.tokenStart, picker.tokenEnd, reference) {
		return nil
	}
	m.refreshFilePicker(true)
	return nil
}

func formatFileReference(path string) string {
	needsQuotes := strings.IndexFunc(path, func(character rune) bool {
		return unicode.IsSpace(character) || character == '"' || character == '\\'
	}) >= 0
	if !needsQuotes {
		return "@" + path
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
	return `@"` + escaped + `"`
}

func (m *tuiModel) visibleFilePickerMatches() []fileSearchMatch {
	matches := m.filePicker.matches
	if len(matches) <= filePickerMaxVisible {
		return matches
	}
	start := m.filePicker.selected - filePickerMaxVisible/2
	start = max(0, min(start, len(matches)-filePickerMaxVisible))
	return matches[start : start+filePickerMaxVisible]
}
