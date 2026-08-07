package terminal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	filePickerMaxResults = 100
	filePickerMaxVisible = 5
)

var errFileSearchComplete = errors.New("file search complete")

type fileSearchRequest struct {
	id    uint64
	query string
}

type fileSearchResult struct {
	id    uint64
	paths []string
}

type fileSearchCommand struct {
	request *fileSearchRequest
	cancel  bool
}

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

func findFD() string {
	for _, name := range []string{"fd", "fdfind"} {
		if executable, err := exec.LookPath(name); err == nil {
			return executable
		}
	}
	return ""
}

type fileSearchRunner struct {
	cwd    string
	fdPath string
	cancel context.CancelFunc
}

func newFileSearchRunner(cwd string) *fileSearchRunner {
	runner := &fileSearchRunner{cwd: cwd}
	if cwd != "" {
		runner.fdPath = findFD()
	}
	return runner
}

func (r *fileSearchRunner) update(ctx context.Context, command fileSearchCommand, output chan<- fileSearchResult) {
	if command.request == nil && !command.cancel {
		return
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if command.request == nil {
		return
	}

	searchContext, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	request := *command.request
	go func() {
		paths, err := searchProjectFiles(searchContext, r.cwd, r.fdPath, request.query)
		if err != nil {
			if searchContext.Err() != nil {
				return
			}
			paths = nil
		}
		select {
		case output <- fileSearchResult{id: request.id, paths: paths}:
		case <-searchContext.Done():
		}
	}()
}

func (r *fileSearchRunner) close() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func searchProjectFiles(ctx context.Context, cwd, fdPath, query string) ([]string, error) {
	if fdPath != "" {
		return searchProjectFilesWithFD(ctx, cwd, fdPath, query)
	}
	return searchProjectFilesWithWalk(ctx, cwd, query)
}

func searchProjectFilesWithFD(ctx context.Context, cwd, fdPath, query string) ([]string, error) {
	arguments := []string{
		"--base-directory", cwd,
		"--max-results", "100",
		"--type", "f",
		"--hidden",
		"--follow",
		"--exclude", ".git",
		"--print0",
		"--fixed-strings",
		"--ignore-case",
	}
	if strings.Contains(query, "/") {
		arguments = append(arguments, "--full-path")
	}
	if query != "" {
		arguments = append(arguments, "--", query)
	}

	command := exec.CommandContext(ctx, fdPath, arguments...)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return parseFDPaths(output), nil
}

func parseFDPaths(output []byte) []string {
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, min(len(parts), filePickerMaxResults))
	for _, part := range parts {
		if normalized, ok := normalizeDiscoveredPath(string(part)); ok {
			paths = append(paths, normalized)
			if len(paths) >= filePickerMaxResults {
				break
			}
		}
	}
	return paths
}

func searchProjectFilesWithWalk(ctx context.Context, cwd, query string) ([]string, error) {
	var paths []string
	normalizedQuery := strings.ToLower(filepath.ToSlash(query))
	fullPath := strings.Contains(normalizedQuery, "/")
	err := filepath.WalkDir(cwd, func(filePath string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if filePath == cwd {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filePath != cwd && entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}

		relative, err := filepath.Rel(cwd, filePath)
		if err != nil {
			return nil
		}
		display, ok := normalizeDiscoveredPath(relative)
		if !ok {
			return nil
		}
		candidate := path.Base(display)
		if fullPath {
			candidate = display
		}
		if normalizedQuery != "" && !strings.Contains(strings.ToLower(candidate), normalizedQuery) {
			return nil
		}

		paths = append(paths, display)
		if len(paths) >= filePickerMaxResults {
			return errFileSearchComplete
		}
		return nil
	})
	if errors.Is(err, errFileSearchComplete) {
		err = nil
	}
	return paths, err
}

func normalizeDiscoveredPath(value string) (string, bool) {
	if value == "" || !utf8.ValidString(value) {
		return "", false
	}
	value = strings.TrimPrefix(filepath.ToSlash(value), "./")
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") {
		return "", false
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	return value, true
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
	request := fileSearchRequest{id: m.filePicker.requestID, query: query}
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
	return maximumFilePickerHeight(m.height) > 0 && m.filePicker.active && (m.filePicker.loading || len(m.filePicker.matches) > 0)
}

func (m *tuiModel) filePickerHeight() int {
	if !m.filePickerVisible() {
		return 0
	}
	if len(m.filePicker.matches) > 0 {
		return min(filePickerMaxVisible, len(m.filePicker.matches))
	}
	return 1
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
