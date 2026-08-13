package terminal

import (
	"strconv"
	"strings"
)

const (
	ansiHideCursor              = "\x1b[?25l"
	ansiShowCursor              = "\x1b[?25h"
	ansiBeginSynchronizedOutput = "\x1b[?2026h"
	ansiEndSynchronizedOutput   = "\x1b[?2026l"
	ansiClearScreen             = "\x1b[2J"
	ansiScrollUp                = "\x1b[1S"
	ansiScrollDown              = "\x1b[1T"
	ansiResetScrollRegion       = "\x1b[r"
)

type renderedConversationBlock struct {
	block         conversationBlock
	lines         []styledLine
	plain         []string
	continuations []bool
}

type tuiRenderer struct {
	frame                     terminalFrame
	conversationBlocks        []renderedConversationBlock
	conversationLines         []styledLine
	conversationPlain         []string
	conversationContinuations []bool
	conversationWidth         int
	conversationVersion       uint64
}

func (r *tuiRenderer) render(model *tuiModel) string {
	output, next := r.renderPending(model, false)
	r.commit(next)
	return output
}

func (r *tuiRenderer) renderPending(model *tuiModel, forceRedraw bool) (string, terminalFrame) {
	prepared := r.prepare(model)
	next := projectTerminalFrame(model, prepared)
	if next.width < 1 || next.height < 1 {
		return "", next
	}

	previous := r.frame
	resized := previous.width != 0 && (previous.width != next.width || previous.height != next.height)
	full := previous.width == 0 || resized || forceRedraw || len(previous.rows) != len(next.rows)
	scroll, scrolling := conversationScrollUpdate(previous, next, full)
	changed := make([]int, 0, len(next.rows))
	for index, row := range next.rows {
		if scrolling && index < next.layout.conversationHeight {
			if index == scroll.exposedRow {
				changed = append(changed, index)
			}
			continue
		}
		if full || row != previous.rows[index] {
			changed = append(changed, index)
		}
	}
	cursorChanged := previous.cursorRow != next.cursorRow || previous.cursorColumn != next.cursorColumn || previous.cursorVisible != next.cursorVisible
	if len(changed) == 0 && !cursorChanged {
		return "", next
	}

	var output strings.Builder
	output.WriteString(ansiBeginSynchronizedOutput)
	output.WriteString(ansiHideCursor)
	if resized || forceRedraw {
		output.WriteString(ansiClearScreen)
	}
	if scrolling {
		writeConversationScroll(&output, next.layout.conversationHeight, scroll.delta)
	}
	for _, index := range changed {
		output.WriteString(next.rows[index])
	}
	if next.cursorVisible {
		writeCursorPosition(&output, next.cursorRow, next.cursorColumn)
		output.WriteString(ansiShowCursor)
	}
	output.WriteString(ansiEndSynchronizedOutput)
	return output.String(), next
}

type conversationScroll struct {
	delta      int
	exposedRow int
}

func conversationScrollUpdate(previous, next terminalFrame, full bool) (conversationScroll, bool) {
	height := next.layout.conversationHeight
	if full || height < 2 || previous.layout != next.layout || previous.conversationVersion != next.conversationVersion {
		return conversationScroll{}, false
	}

	delta := next.conversationTop - previous.conversationTop
	switch delta {
	case 1:
		for row := 0; row < height-1; row++ {
			if !sameRenderedRow(next.rows[row], row+1, previous.rows[row+1], row+2) {
				return conversationScroll{}, false
			}
		}
		return conversationScroll{delta: delta, exposedRow: height - 1}, true
	case -1:
		for row := 1; row < height; row++ {
			if !sameRenderedRow(next.rows[row], row+1, previous.rows[row-1], row) {
				return conversationScroll{}, false
			}
		}
		return conversationScroll{delta: delta}, true
	default:
		return conversationScroll{}, false
	}
}

func sameRenderedRow(left string, leftRow int, right string, rightRow int) bool {
	left, leftOK := strings.CutPrefix(left, "\x1b["+strconv.Itoa(leftRow)+";1H")
	right, rightOK := strings.CutPrefix(right, "\x1b["+strconv.Itoa(rightRow)+";1H")
	return leftOK && rightOK && left == right
}

func writeConversationScroll(output *strings.Builder, height, delta int) {
	output.WriteString("\x1b[1;")
	output.WriteString(strconv.Itoa(height))
	output.WriteByte('r')
	if delta > 0 {
		output.WriteString(ansiScrollUp)
	} else {
		output.WriteString(ansiScrollDown)
	}
	output.WriteString(ansiResetScrollRegion)
}

func (r *tuiRenderer) commit(frame terminalFrame) {
	r.frame = frame
}
