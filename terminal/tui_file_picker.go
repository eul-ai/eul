package terminal

import (
	"strings"
	"unicode"
)

const filePickerMaxVisible = 5

type filePickerState struct {
	matches      []string
	query        string
	tokenStart   int
	tokenEnd     int
	selected     int
	requestID    uint64
	pending      *fileSearchRequest
	enabled      bool
	active       bool
	loading      bool
	dismissed    bool
	cancelSearch bool
}

func fileReferenceToken(input []rune, cursor int) (int, string, bool) {
	if cursor < 0 || cursor > len(input) {
		return 0, "", false
	}

	start := cursor
	for start > 0 && !unicode.IsSpace(input[start-1]) {
		start--
	}
	if start >= cursor || input[start] != '@' {
		return 0, "", false
	}
	return start, string(input[start+1 : cursor]), true
}

func (m *tuiModel) refreshFilePicker(reopen bool) {
	if !m.filePicker.enabled {
		m.clearFilePicker()
		return
	}
	start, query, ok := fileReferenceToken(m.input, m.cursor)
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
	m.filePicker.tokenEnd = m.cursor
	if wasActive && query == previousQuery {
		return
	}

	m.filePicker.query = query
	m.filePicker.loading = true
	m.filePicker.requestID++
	searchQuery := query
	root := fileSearchProject
	switch {
	case strings.HasPrefix(query, "~"):
		searchQuery = strings.TrimPrefix(strings.TrimPrefix(query, "~"), "/")
		root = fileSearchHome
	case strings.HasPrefix(query, "/"):
		searchQuery = strings.TrimPrefix(query, "/")
		root = fileSearchAbsolute
	}
	request := fileSearchRequest{id: m.filePicker.requestID, query: searchQuery, root: root}
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
		selectedPath = m.filePicker.matches[m.filePicker.selected]
	}
	m.filePicker.loading = false
	m.filePicker.matches = append([]string(nil), result.paths...)
	m.filePicker.selected = 0
	if selectedPath != "" {
		for index, match := range m.filePicker.matches {
			if match == selectedPath {
				m.filePicker.selected = index
				break
			}
		}
	}
	return true
}

func (m *tuiModel) clearFilePicker() {
	enabled := m.filePicker.enabled
	cancelSearch := m.filePicker.loading || m.filePicker.pending != nil
	requestID := m.filePicker.requestID + 1
	m.filePicker = filePickerState{
		enabled:      enabled,
		requestID:    requestID,
		cancelSearch: cancelSearch,
	}
}

func (m *tuiModel) dismissFilePicker() {
	m.filePicker.active = false
	m.filePicker.loading = false
	m.filePicker.matches = nil
	m.filePicker.pending = nil
	m.filePicker.dismissed = true
	m.filePicker.requestID++
	m.filePicker.cancelSearch = true
}

func (m *tuiModel) filePickerVisible() bool {
	return maximumFilePickerHeight(m.height) > 0 && m.filePicker.active
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
	if picker.selected < 0 || picker.selected >= len(picker.matches) {
		return nil
	}

	reference := formatFileReference(picker.matches[picker.selected]) + " "
	before := string(m.input[:picker.tokenStart])
	after := string(m.input[picker.tokenEnd:])
	if len(before)+len(reference)+len(after) > maxInputBytes {
		return errInputTooLong
	}

	m.leaveHistory()
	m.input = []rune(before + reference + after)
	m.cursor = len([]rune(before + reference))
	m.clearFilePicker()
	return nil
}

func formatFileReference(path string) string {
	needsQuotes := strings.IndexFunc(path, func(character rune) bool {
		return unicode.IsSpace(character) || character == '"'
	}) >= 0
	if !needsQuotes {
		return "@" + path
	}
	return `@"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

func (m *tuiModel) visibleFilePickerMatches() []string {
	matches := m.filePicker.matches
	if len(matches) <= filePickerMaxVisible {
		return matches
	}
	start := m.filePicker.selected - filePickerMaxVisible/2
	start = max(0, min(start, len(matches)-filePickerMaxVisible))
	return matches[start : start+filePickerMaxVisible]
}
