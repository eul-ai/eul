package terminal

import (
	"errors"
	"os"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/filesearch"
)

const filePickerMaxVisible = 5

type filePickerState struct {
	matches        []filesearch.Match
	matchesCurrent bool
	query          string
	tokenStart     int
	tokenEnd       int
	selected       int
	requestID      uint64
	pending        *filesearch.Request
	state          filesearch.State
	err            string
	enabled        bool
	active         bool
	dismissed      bool
	cancelSearch   bool
}

type fileSearchCommand struct {
	request *filesearch.Request
	cancel  bool
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
			query := decodeFileReferenceQuery(string(input[start+1 : end]))
			return start, end, query, true
		}
		start = end + 1
	}
	return 0, 0, "", false
}

func decodeFileReferenceQuery(value string) string {
	if !strings.HasPrefix(value, `"`) {
		return value
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
	return decoded.String()
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
		m.filePicker.state = filesearch.StateDiscovering
		m.filePicker.err = ""
	}
	m.filePicker.matchesCurrent = false
	m.filePicker.requestID++
	request := filesearch.Request{ID: m.filePicker.requestID, Query: query, Refresh: !wasActive}
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

func (m *tuiModel) applyFileSearchResult(result filesearch.Result) bool {
	if result.ID != m.filePicker.requestID || !m.filePicker.active {
		return false
	}
	selectedPath := ""
	if m.filePicker.selected >= 0 && m.filePicker.selected < len(m.filePicker.matches) {
		selectedPath = fileSearchMatchIdentity(m.filePicker.matches[m.filePicker.selected])
	}
	m.filePicker.state = result.State
	m.filePicker.err = ""
	if result.Err != nil {
		m.filePicker.err = result.Err.Error()
	}
	m.filePicker.matches = append([]filesearch.Match(nil), result.Matches...)
	m.filePicker.matchesCurrent = true
	m.filePicker.selected = 0
	if selectedPath != "" {
		for index, match := range m.filePicker.matches {
			if fileSearchMatchIdentity(match) == selectedPath {
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

	reference := formatFileReference(match.Reference)
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

func validateFileSearchMatch(match filesearch.Match) error {
	if match.Path == "" {
		return nil
	}
	if _, err := os.Lstat(match.Path); err != nil {
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
	if picker.selected < 0 || picker.selected >= len(picker.matches) || !picker.matches[picker.selected].IsDir {
		return nil
	}
	match := picker.matches[picker.selected]
	if err := validateFileSearchMatch(match); err != nil {
		return err
	}

	reference := formatFileReference(match.BrowseQuery)
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

func (m *tuiModel) visibleFilePickerMatches() []filesearch.Match {
	matches := m.filePicker.matches
	if len(matches) <= filePickerMaxVisible {
		return matches
	}
	start := m.filePicker.selected - filePickerMaxVisible/2
	start = max(0, min(start, len(matches)-filePickerMaxVisible))
	return matches[start : start+filePickerMaxVisible]
}

func fileSearchMatchIdentity(match filesearch.Match) string {
	if match.Path != "" {
		return match.Path
	}
	return match.Reference
}
