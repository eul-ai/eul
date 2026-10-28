package terminal

import (
	"slices"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/session"
)

type Checkpoint struct {
	Entries      []session.Entry `json:"entries,omitempty"`
	Input        string          `json:"input,omitempty"`
	Cursor       int             `json:"cursor,omitempty"`
	QueuedInputs []string        `json:"queued_inputs,omitempty"`
}

func checkpointModel(model *tuiModel, queued [][]agent.ContentPart) Checkpoint {
	var queuedInputs []string
	for _, content := range queued {
		text := contentText(content)
		if strings.TrimSpace(text) != "" {
			queuedInputs = append(queuedInputs, text)
		}
	}

	return Checkpoint{
		Entries:      slices.Clone(model.transcript.Entries),
		Input:        model.inputText(),
		Cursor:       model.textCursor(),
		QueuedInputs: queuedInputs,
	}
}

func restoreModelCheckpoint(model *tuiModel, checkpoint Checkpoint) {
	model.transcript = session.RestoreTranscript(checkpoint.Entries)
	model.conversationChanged()

	model.input = editorItemsFromText(checkpoint.Input)
	model.cursor = min(max(checkpoint.Cursor, 0), len(model.input))
	if len(checkpoint.QueuedInputs) > 0 {
		queued := strings.Join(checkpoint.QueuedInputs, "\n\n")
		if strings.TrimSpace(checkpoint.Input) != "" {
			queued += "\n\n" + checkpoint.Input
		}
		model.input = editorItemsFromText(queued)
		model.cursor = len(model.input)
	}
}
