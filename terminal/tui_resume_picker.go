package terminal

import (
	"slices"
	"strings"
)

type resumePickerState struct {
	pickerList[SessionSummary]
	active bool
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
	m.resumePicker = resumePickerState{pickerList: pickerList[SessionSummary]{items: summaries}, active: true}
}

func (m *tuiModel) dismissResumePicker() {
	m.resumePicker = resumePickerState{}
}

func (m *tuiModel) resumePickerVisible() bool {
	return maximumPickerHeight(m.height) > 0 && m.resumePicker.active
}

func resumeSummaryLabel(summary SessionSummary) string {
	if description := strings.TrimSpace(summary.Description); description != "" {
		return description
	}
	return summary.ID
}
