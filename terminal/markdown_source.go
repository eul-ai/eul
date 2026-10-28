package terminal

import (
	"slices"
	"strings"
	"unicode/utf8"
)

type sourceRange struct {
	start int
	end   int
}

func (r sourceRange) empty() bool {
	return r.end <= r.start
}

type markdownFence struct {
	opening string
	content sourceRange
}

// markdownSourceMap links every cell rendered by wrapMarkdown to the markdown source bytes it displays.
type markdownSourceMap struct {
	source    string
	rows      [][]sourceRange
	displayed []bool
	fences    []markdownFence
}

func newMarkdownSourceMap(source string, width int) markdownSourceMap {
	sourceLines := strings.Split(source, "\n")
	lineStarts := make([]int, len(sourceLines)+1)
	for index, line := range sourceLines {
		lineStarts[index+1] = lineStarts[index] + len(line) + 1
	}
	lineEnd := func(index int) int {
		return lineStarts[index+1] - 1
	}
	lineContent := func(block markdownBlock) []sourceRange {
		content := block.lines[0]
		return inlineSourceText(content, lineEnd(block.lineIndex)-len(content))
	}
	lineMarker := func(block markdownBlock) sourceRange {
		return sourceRange{start: lineStarts[block.lineIndex], end: lineEnd(block.lineIndex) - len(block.lines[0])}
	}

	sourceMap := markdownSourceMap{source: source, displayed: make([]bool, len(source))}
	for _, block := range parseMarkdownBlocks(source) {
		rows := wrapMarkdownBlock(block, width)
		start := lineStarts[block.lineIndex]
		switch block.kind {
		case markdownBlockParagraph, markdownBlockBlank:
			sourceMap.addWrappedRows(rows, inlineSourceText(strings.Join(block.lines, "\n"), start), 0, sourceRange{})
		case markdownBlockHeading:
			sourceMap.addWrappedRows(rows, lineContent(block), 0, sourceRange{})
		case markdownBlockBlockQuote:
			prefix := len(markdownQuotePrefix(block.quoteDepth, width))
			sourceMap.addWrappedRows(rows, lineContent(block), prefix, lineMarker(block))
		case markdownBlockListItem:
			text := lineContent(block)
			if cellWidth(block.listPrefix) >= width {
				marker := slices.Repeat([]sourceRange{lineMarker(block)}, len(block.listPrefix))
				sourceMap.addWrappedRows(rows, append(marker, text...), 0, sourceRange{})
				continue
			}
			sourceMap.addWrappedRows(rows, text, len(block.listPrefix), lineMarker(block))
		case markdownBlockFencedCode:
			content := strings.Join(block.lines, "\n")
			offsets := make([]int, len(content))
			for index := range offsets {
				offsets[index] = start + index
			}
			sourceMap.addWrappedRows(rows, wrappedSourceText(content, offsets), 0, sourceRange{})
			sourceMap.fences = append(sourceMap.fences, markdownFence{
				opening: sourceLines[block.lineIndex-1],
				content: sourceRange{start: start, end: start + len(content)},
			})
		case markdownBlockTable:
			sourceMap.addAtomicRows(rows, sourceRange{start: start, end: lineEnd(block.lineIndex + len(block.table.rows))})
		case markdownBlockThematicBreak:
			sourceMap.addAtomicRows(rows, sourceRange{start: start, end: lineEnd(block.lineIndex)})
		}
	}
	return sourceMap
}

func inlineSourceText(content string, start int) []sourceRange {
	var offsets []int
	spans := parseInlineMarkdownSource(content, inlineStyle{}, start, &offsets)
	return wrappedSourceText(inlineSpanText(spans), offsets)
}

// wrappedSourceText expands tabs and invalid UTF-8 the way wrapping does and returns the source range behind every
// byte of the expanded text.
func wrappedSourceText(text string, offsets []int) []sourceRange {
	wrapped := make([]sourceRange, 0, len(text))
	for index := 0; index < len(text); {
		character, size := utf8.DecodeRuneInString(text[index:])
		source := sourceRange{start: offsets[index], end: offsets[index] + size}
		length := size
		switch {
		case character == '\t':
			length = len("    ")
		case character == utf8.RuneError && size == 1:
			length = utf8.RuneLen(utf8.RuneError)
		}
		for range length {
			wrapped = append(wrapped, source)
		}
		index += size
	}
	return wrapped
}

// addWrappedRows maps rows wrapped from text, where every row starts with prefix bytes of layout that is not part of
// text. The prefix of the first row displays marker, such as a list bullet; later prefixes only indent.
func (m *markdownSourceMap) addWrappedRows(rows []formattedLine, text []sourceRange, prefix int, marker sourceRange) {
	m.markDisplayed(marker)
	for _, source := range text {
		m.markDisplayed(source)
	}

	position := 0
	for index, row := range rows {
		if index > 0 {
			position += len(row.breakBefore.sourceSeparator())
		}
		cells := make([]sourceRange, 0, len(row.text))
		for offset, character := range row.text {
			var source sourceRange
			switch textOffset := position + offset - prefix; {
			case offset < prefix && index == 0:
				source = marker
			case offset >= prefix && textOffset < len(text):
				source = text[textOffset]
			}
			width := runeWidth(character)
			if previous := len(cells) - 1; width == 0 && previous >= 0 && !cells[previous].empty() && !source.empty() {
				cells[previous].end = source.end
			}
			for range width {
				cells = append(cells, source)
			}
		}
		position += max(0, len(row.text)-prefix)
		m.rows = append(m.rows, cells)
	}
}

func (m *markdownSourceMap) addAtomicRows(rows []formattedLine, source sourceRange) {
	m.markDisplayed(source)
	for _, row := range rows {
		cells := make([]sourceRange, cellWidth(row.text))
		for index := range cells {
			cells[index] = source
		}
		m.rows = append(m.rows, cells)
	}
}

func (m *markdownSourceMap) markDisplayed(source sourceRange) {
	for offset := source.start; offset < source.end; offset++ {
		m.displayed[offset] = true
	}
}

// selection returns the markdown behind the cells from startColumn on startRow through endColumn on endRow. Markup
// that directly encloses the selected text, such as emphasis markers or a heading marker, is included.
func (m markdownSourceMap) selection(startRow, startColumn, endRow, endColumn int) string {
	start, end := len(m.source), 0
	for row := max(0, startRow); row <= min(endRow, len(m.rows)-1); row++ {
		cells := m.rows[row]
		first, last := 0, len(cells)-1
		if row == startRow {
			first = max(first, startColumn)
		}
		if row == endRow {
			last = min(last, endColumn)
		}
		for column := first; column <= last; column++ {
			if cell := cells[column]; !cell.empty() {
				start = min(start, cell.start)
				end = max(end, cell.end)
			}
		}
	}
	if end <= start {
		return ""
	}

	for start > 0 && !m.displayed[start-1] && m.source[start-1] != '\n' {
		start--
	}
	for end < len(m.source) && !m.displayed[end] && m.source[end] != '\n' {
		end++
	}

	var opening, closing string
	for _, fence := range m.fences {
		startsInside := start >= fence.content.start && start <= fence.content.end
		endsInside := end >= fence.content.start && end <= fence.content.end
		switch {
		case startsInside && !endsInside:
			opening = fence.opening + "\n"
		case endsInside && !startsInside:
			closing = "\n```"
		}
	}
	return opening + m.source[start:end] + closing
}
