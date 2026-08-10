package terminal

import (
	"slices"
	"strings"
)

const resumePickerMaxVisible = 5

type resumePickerState struct {
	matches  []SessionSummary
	selected int
	active   bool
}

func (m *tuiModel) openResumePicker(summaries []SessionSummary) {
	summaries = append([]SessionSummary(nil), summaries...)
	slices.SortFunc(summaries, func(left, right SessionSummary) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	m.clearInputPickers()
	m.resumePicker = resumePickerState{matches: summaries, active: true}
}

func (m *tuiModel) dismissResumePicker() {
	m.resumePicker = resumePickerState{}
}

func (m *tuiModel) resumePickerVisible() bool {
	return maximumPickerHeight(m.height) > 0 && m.resumePicker.active
}

func (m *tuiModel) resumePickerHeight() int {
	if !m.resumePickerVisible() {
		return 0
	}
	if len(m.resumePicker.matches) == 0 {
		return 1
	}
	return min(resumePickerMaxVisible, len(m.resumePicker.matches))
}

func (m *tuiModel) moveResumePickerSelection(direction int) {
	count := len(m.resumePicker.matches)
	if count == 0 {
		return
	}
	m.resumePicker.selected = (m.resumePicker.selected + direction + count) % count
}

func (m *tuiModel) selectedResumeSession() (string, bool) {
	selected := m.resumePicker.selected
	if selected < 0 || selected >= len(m.resumePicker.matches) {
		return "", false
	}
	return m.resumePicker.matches[selected].ID, true
}

func (m *tuiModel) visibleResumePickerMatches() []SessionSummary {
	matches := m.resumePicker.matches
	if len(matches) <= resumePickerMaxVisible {
		return matches
	}
	start := m.resumePicker.selected - resumePickerMaxVisible/2
	start = max(0, min(start, len(matches)-resumePickerMaxVisible))
	return matches[start : start+resumePickerMaxVisible]
}

func resumeSummaryLabel(summary SessionSummary) string {
	if description := strings.TrimSpace(summary.Description); description != "" {
		return description
	}
	return summary.ID
}
