package terminal

func modelConversationLines(model *tuiModel, width int) []styledLine {
	blocks := append([]conversationBlock(nil), model.blocks...)
	for _, message := range model.pendingSteering() {
		blocks = append(blocks, conversationBlock{kind: blockInfo, text: "Queued: " + message})
	}
	lines := conversationLines(blocks, width)
	result := make([]styledLine, 0, len(lines)+conversationVerticalPadding*2)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	result = append(result, lines...)
	result = append(result, make([]styledLine, conversationVerticalPadding)...)
	return result
}
