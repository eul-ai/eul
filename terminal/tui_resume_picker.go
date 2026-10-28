package terminal

import (
	"slices"
	"strings"

	"github.com/eul-ai/eul/session"
)

type resumePickerState struct {
	pickerList[session.Summary]
	active bool
}

func (m *tuiModel) openResumePicker(summaries []session.Summary) {
	summaries = append([]session.Summary(nil), summaries...)
	slices.SortFunc(summaries, func(left, right session.Summary) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	m.clearInputPickers()
	m.resumePicker = resumePickerState{pickerList: pickerList[session.Summary]{items: summaries}, active: true}
}

func (m *tuiModel) dismissResumePicker() {
	m.resumePicker = resumePickerState{}
}

func (m *tuiModel) resumePickerVisible() bool {
	return maximumPickerHeight(m.height) > 0 && m.resumePicker.active
}

func resumeSummaryLabel(summary session.Summary) string {
	if description := strings.TrimSpace(summary.Description); description != "" {
		return description
	}
	return summary.ID
}
