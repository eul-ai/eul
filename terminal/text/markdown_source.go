package text

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

// markdownSourceMap links every cell rendered by WrapMarkdown to the markdown source bytes it displays.
type markdownSourceMap struct {
	source    string
	rows      [][]sourceRange
	displayed []bool
	fences    []markdownFence
}

// MarkdownSelection returns the markdown behind the cells from startColumn on startRow through endColumn on endRow
// of source rendered by WrapMarkdown at width. Markup that directly encloses the selected text, such as emphasis
// markers or a heading marker, is included.
func MarkdownSelection(source string, width, startRow, startColumn, endRow, endColumn int) string {
	return newMarkdownSourceMap(source, width).selection(startRow, startColumn, endRow, endColumn)
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
			content := lineContent(block)
			if Width(block.listPrefix) >= width {
				marker := slices.Repeat([]sourceRange{lineMarker(block)}, len(block.listPrefix))
				sourceMap.addWrappedRows(rows, append(marker, content...), 0, sourceRange{})
				continue
			}
			sourceMap.addWrappedRows(rows, content, len(block.listPrefix), lineMarker(block))
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
	spans := parseInlineSource(content, Style{}, start, &offsets)
	return wrappedSourceText(Plain(spans), offsets)
}

// wrappedSourceText expands tabs and invalid UTF-8 the way wrapping does and returns the source range behind every
// byte of the expanded text.
func wrappedSourceText(value string, offsets []int) []sourceRange {
	wrapped := make([]sourceRange, 0, len(value))
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
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

// addWrappedRows maps rows wrapped from content, where every row starts with prefix bytes of layout that is not part
// of content. The prefix of the first row displays marker, such as a list bullet; later prefixes only indent.
func (m *markdownSourceMap) addWrappedRows(rows []Line, content []sourceRange, prefix int, marker sourceRange) {
	m.markDisplayed(marker)
	for _, source := range content {
		m.markDisplayed(source)
	}

	position := 0
	for index, row := range rows {
		if index > 0 {
			position += len(row.Break.SourceSeparator())
		}
		rowText := row.Text()
		cells := make([]sourceRange, 0, len(rowText))
		for offset, character := range rowText {
			var source sourceRange
			switch contentOffset := position + offset - prefix; {
			case offset < prefix && index == 0:
				source = marker
			case offset >= prefix && contentOffset < len(content):
				source = content[contentOffset]
			}
			width := RuneWidth(character)
			if previous := len(cells) - 1; width == 0 && previous >= 0 && !cells[previous].empty() && !source.empty() {
				cells[previous].end = source.end
			}
			for range width {
				cells = append(cells, source)
			}
		}
		position += max(0, len(rowText)-prefix)
		m.rows = append(m.rows, cells)
	}
}

func (m *markdownSourceMap) addAtomicRows(rows []Line, source sourceRange) {
	m.markDisplayed(source)
	for _, row := range rows {
		cells := make([]sourceRange, Width(row.Text()))
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
