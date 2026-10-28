package terminal

func modelConversationLines(model *tuiModel, width int) []styledLine {
	blocks := append([]conversationBlock(nil), model.blocks...)
	for _, content := range model.pendingSteering() {
		blocks = append(blocks, conversationBlock{kind: blockInfo, text: "Queued: " + displayContent(content) + " (alt+↑ to restore)"})
	}
	lines := conversationLines(blocks, width)
	result := make([]styledLine, 0, len(lines)+conversationVerticalPadding*2)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	result = append(result, lines...)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	return result
}

func conversationLines(blocks []conversationBlock, width int) []styledLine {
	var lines []styledLine
	for index, block := range blocks {
		lines = append(lines, conversationBlockLines(block, width)...)
		if index < len(blocks)-1 {
			lines = append(lines, styledLine{})
		}
	}
	return lines
}
