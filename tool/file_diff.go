package tool

import (
	"sort"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const (
	fileDiffContextLines     = 4
	fileDiffTruncationMarker = "… (diff truncated)"
	fileDiffHunkMarker       = "..."
)

type diffText struct {
	text     string
	newlines []int
}

type diffBlock struct {
	oldFirst, oldEnd int
	newFirst, newEnd int
}

type fileDiffBuilder struct {
	lines []agent.ToolDiffLine
	bytes int
	full  bool
}

// buildFileDiff shows the lines changed by replacing the oldLength bytes at
// each of starts in original, which produced replacement.
func buildFileDiff(original, replacement []byte, starts []int, oldLength, newLength int) []agent.ToolDiffLine {
	before := newDiffText(original)
	after := newDiffText(replacement)
	blocks := diffBlocks(before, after, starts, oldLength, newLength)

	var builder fileDiffBuilder
	for index, block := range blocks {
		offset := block.newFirst - block.oldFirst
		contextStart := max(0, block.oldFirst-fileDiffContextLines)
		if index > 0 {
			contextStart = blocks[index-1].oldEnd
		}
		if block.oldFirst-contextStart > 2*fileDiffContextLines {
			builder.appendRange(agent.ToolDiffLineContext, before, contextStart, contextStart+fileDiffContextLines, offset)
			builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineOmitted, Text: fileDiffHunkMarker})
			contextStart = block.oldFirst - fileDiffContextLines
		}
		builder.appendRange(agent.ToolDiffLineContext, before, contextStart, block.oldFirst, offset)
		builder.appendRange(agent.ToolDiffLineRemoved, before, block.oldFirst, block.oldEnd, 0)
		builder.appendRange(agent.ToolDiffLineAdded, after, block.newFirst, block.newEnd, 0)
	}
	if len(blocks) > 0 {
		last := blocks[len(blocks)-1]
		builder.appendRange(agent.ToolDiffLineContext, before, last.oldEnd, min(before.lineCount(), last.oldEnd+fileDiffContextLines), last.newEnd-last.oldEnd)
	}
	return builder.lines
}

func diffBlocks(before, after diffText, starts []int, oldLength, newLength int) []diffBlock {
	var enclosing []diffBlock
	for index, start := range starts {
		newStart := start + index*(newLength-oldLength)
		block := diffBlock{
			oldFirst: before.lineIndex(start),
			oldEnd:   min(before.lineIndex(start+oldLength)+1, before.lineCount()),
			newFirst: after.lineIndex(newStart),
			newEnd:   min(after.lineIndex(newStart+newLength)+1, after.lineCount()),
		}
		if last := len(enclosing) - 1; last >= 0 && block.oldFirst < enclosing[last].oldEnd {
			enclosing[last].oldEnd, enclosing[last].newEnd = block.oldEnd, block.newEnd
			continue
		}
		enclosing = append(enclosing, block)
	}

	blocks := enclosing[:0]
	for _, block := range enclosing {
		for block.oldFirst < block.oldEnd && block.newFirst < block.newEnd && before.line(block.oldFirst) == after.line(block.newFirst) {
			block.oldFirst++
			block.newFirst++
		}
		for block.oldFirst < block.oldEnd && block.newFirst < block.newEnd && before.line(block.oldEnd-1) == after.line(block.newEnd-1) {
			block.oldEnd--
			block.newEnd--
		}
		switch last := len(blocks) - 1; {
		case block.oldFirst == block.oldEnd && block.newFirst == block.newEnd:
		case last >= 0 && block.oldFirst <= blocks[last].oldEnd:
			blocks[last].oldEnd, blocks[last].newEnd = block.oldEnd, block.newEnd
		default:
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func newDiffText(content []byte) diffText {
	text := string(content)
	var newlines []int
	for offset, character := range content {
		if character == '\n' {
			newlines = append(newlines, offset)
		}
	}
	return diffText{text: text, newlines: newlines}
}

func (t diffText) lineIndex(offset int) int {
	return sort.SearchInts(t.newlines, offset)
}

func (t diffText) lineCount() int {
	if t.text == "" || strings.HasSuffix(t.text, "\n") {
		return len(t.newlines)
	}
	return len(t.newlines) + 1
}

func (t diffText) line(index int) string {
	start, end := 0, len(t.text)
	if index > 0 {
		start = t.newlines[index-1] + 1
	}
	if index < len(t.newlines) {
		end = t.newlines[index] + 1
	}
	return t.text[start:end]
}

func (t diffText) display(index int) string {
	line, terminated := strings.CutSuffix(t.line(index), "\n")
	if terminated {
		return strings.TrimSuffix(line, "\r")
	}
	return line
}

func (builder *fileDiffBuilder) appendRange(kind agent.ToolDiffLineKind, text diffText, first, end, newOffset int) {
	for index := first; index < end && !builder.full; index++ {
		line := agent.ToolDiffLine{Kind: kind, Text: text.display(index)}
		switch kind {
		case agent.ToolDiffLineContext:
			line.OldLine, line.NewLine = index+1, index+newOffset+1
		case agent.ToolDiffLineRemoved:
			line.OldLine = index + 1
		case agent.ToolDiffLineAdded:
			line.NewLine = index + 1
		}
		builder.append(line)
	}
}

func (builder *fileDiffBuilder) append(line agent.ToolDiffLine) {
	if builder.full {
		return
	}

	bodyBytes := defaultMaxBytes - len(fileDiffTruncationMarker)
	if len(builder.lines) < defaultMaxLines-1 && builder.bytes+len(line.Text) <= bodyBytes {
		builder.lines = append(builder.lines, line)
		builder.bytes += len(line.Text)
		return
	}

	remainingBytes := bodyBytes - builder.bytes
	if len(builder.lines) < defaultMaxLines-1 && remainingBytes > 0 {
		line.Text = prefixBytes(line.Text, remainingBytes)
		builder.lines = append(builder.lines, line)
	}
	builder.lines = append(builder.lines, agent.ToolDiffLine{Kind: agent.ToolDiffLineOmitted, Text: fileDiffTruncationMarker})
	builder.full = true
}
