package terminal

import (
	"strings"

	"github.com/eul-ai/eul/session"
)

func (m *tuiModel) draft() session.Draft {
	return session.Draft{Input: m.inputText(), Cursor: m.textCursor()}
}

func (m *tuiModel) restoreDraft(draft session.Draft) {
	m.input = editorItemsFromText(draft.Input)
	m.cursor = min(max(draft.Cursor, 0), len(m.input))
	if len(draft.QueuedInputs) == 0 {
		return
	}

	queued := strings.Join(draft.QueuedInputs, "\n\n")
	if strings.TrimSpace(draft.Input) != "" {
		queued += "\n\n" + draft.Input
	}
	m.input = editorItemsFromText(queued)
	m.cursor = len(m.input)
}
