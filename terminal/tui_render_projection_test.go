package terminal

import "github.com/eul-ai/eul/session"

func modelConversationLines(model *tuiModel, width int) []styledLine {
	blocks := append([]session.Entry(nil), model.entries...)
	for _, content := range model.queued {
		blocks = append(blocks, session.Entry{Kind: session.EntryInfo, Text: "Queued: " + displayContent(content) + " (alt+↑ to restore)"})
	}
	lines := conversationLines(blocks, width)
	result := make([]styledLine, 0, len(lines)+conversationVerticalPadding*2)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	result = append(result, lines...)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	return result
}

func conversationLines(blocks []session.Entry, width int) []styledLine {
	var lines []styledLine
	for index, block := range blocks {
		lines = append(lines, conversationBlockLines(block, width)...)
		if index < len(blocks)-1 {
			lines = append(lines, styledLine{})
		}
	}
	return lines
}
