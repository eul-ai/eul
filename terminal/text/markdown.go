package text

import "strings"

type markdownBlockKind uint8

const (
	markdownBlockParagraph markdownBlockKind = iota
	markdownBlockBlank
	markdownBlockFencedCode
	markdownBlockHeading
	markdownBlockThematicBreak
	markdownBlockBlockQuote
	markdownBlockListItem
	markdownBlockTable
)

type markdownBlock struct {
	kind         markdownBlockKind
	lines        []string
	lineIndex    int
	headingLevel int
	quoteDepth   int
	listPrefix   string
	table        markdownTable
}

type markdownTableAlignment uint8

const (
	markdownTableAlignLeft markdownTableAlignment = iota
	markdownTableAlignCenter
	markdownTableAlignRight
)

type markdownTable struct {
	rows       [][]string
	alignments []markdownTableAlignment
}

func WrapMarkdown(source string, width int) []Line {
	if width <= 0 {
		return nil
	}

	var lines []Line
	for _, block := range parseMarkdownBlocks(source) {
		lines = append(lines, wrapMarkdownBlock(block, width)...)
	}
	return lines
}

func parseMarkdownBlocks(source string) []markdownBlock {
	sourceLines := strings.Split(source, "\n")
	blocks := make([]markdownBlock, 0, len(sourceLines))

	for index := 0; index < len(sourceLines); {
		line := sourceLines[index]
		switch {
		case strings.HasPrefix(line, "```"):
			index++
			start := index
			for index < len(sourceLines) && strings.TrimSpace(sourceLines[index]) != "```" {
				index++
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockFencedCode, lines: sourceLines[start:index], lineIndex: start})
			if index < len(sourceLines) {
				index++
			}
		case line == "":
			start := index
			for index < len(sourceLines) && sourceLines[index] == "" {
				index++
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockBlank, lines: sourceLines[start:index], lineIndex: start})
		default:
			if level, content, ok := parseMarkdownHeading(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockHeading, lines: []string{content}, lineIndex: index, headingLevel: level,
				})
				index++
				continue
			}
			if isMarkdownThematicBreak(line) {
				blocks = append(blocks, markdownBlock{kind: markdownBlockThematicBreak, lineIndex: index})
				index++
				continue
			}
			if depth, content, ok := parseMarkdownBlockQuote(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockBlockQuote, lines: []string{content}, lineIndex: index, quoteDepth: depth,
				})
				index++
				continue
			}
			if table, end, ok := parseMarkdownTable(sourceLines, index); ok {
				blocks = append(blocks, markdownBlock{kind: markdownBlockTable, lineIndex: index, table: table})
				index = end
				continue
			}
			if prefix, content, ok := parseMarkdownListItem(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockListItem, lines: []string{content}, lineIndex: index, listPrefix: prefix,
				})
				index++
				continue
			}

			start := index
			for index < len(sourceLines) && !startsMarkdownBlock(sourceLines, index) {
				index++
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockParagraph, lines: sourceLines[start:index], lineIndex: start})
		}
	}
	return blocks
}

func startsMarkdownBlock(lines []string, index int) bool {
	line := lines[index]
	if line == "" || strings.HasPrefix(line, "```") {
		return true
	}
	if _, _, heading := parseMarkdownHeading(line); heading {
		return true
	}
	if isMarkdownThematicBreak(line) {
		return true
	}
	if _, _, blockQuote := parseMarkdownBlockQuote(line); blockQuote {
		return true
	}
	if _, _, listItem := parseMarkdownListItem(line); listItem {
		return true
	}
	_, _, table := parseMarkdownTable(lines, index)
	return table
}

func parseMarkdownHeading(line string) (int, string, bool) {
	start := 0
	for start < len(line) && line[start] == ' ' && start < 4 {
		start++
	}
	if start == 4 || start == len(line) || line[start] != '#' {
		return 0, "", false
	}

	end := start
	for end < len(line) && line[end] == '#' {
		end++
	}
	level := end - start
	if level > 6 || end < len(line) && line[end] != ' ' && line[end] != '\t' {
		return 0, "", false
	}
	return level, strings.TrimLeft(line[end:], " \t"), true
}

func parseMarkdownListItem(line string) (string, string, bool) {
	markerStart := 0
	for markerStart < len(line) && line[markerStart] == ' ' && markerStart < 4 {
		markerStart++
	}
	if markerStart == 4 || markerStart == len(line) || isMarkdownThematicBreak(line[markerStart:]) {
		return "", "", false
	}

	markerEnd := markerStart
	switch {
	case strings.ContainsRune("-*+", rune(line[markerStart])):
		markerEnd++
	case line[markerStart] >= '0' && line[markerStart] <= '9':
		for markerEnd < len(line) && line[markerEnd] >= '0' && line[markerEnd] <= '9' {
			markerEnd++
		}
		if markerEnd == len(line) || line[markerEnd] != '.' {
			return "", "", false
		}
		markerEnd++
	default:
		return "", "", false
	}

	if markerEnd == len(line) {
		return line, "", true
	}
	if line[markerEnd] != ' ' && line[markerEnd] != '\t' {
		return "", "", false
	}

	contentStart := markerEnd
	for contentStart < len(line) && (line[contentStart] == ' ' || line[contentStart] == '\t') {
		contentStart++
	}
	prefix := strings.ReplaceAll(line[:contentStart], "\t", "    ")
	return prefix, line[contentStart:], true
}

func isMarkdownThematicBreak(line string) bool {
	start := 0
	for start < len(line) && line[start] == ' ' && start < 4 {
		start++
	}
	if start == 4 || start == len(line) {
		return false
	}

	marker := rune(line[start])
	if !strings.ContainsRune("-*_", marker) {
		return false
	}

	markers := 0
	for _, character := range line[start:] {
		switch character {
		case marker:
			markers++
		case ' ', '\t':
		default:
			return false
		}
	}
	return markers >= 3
}

func parseMarkdownBlockQuote(line string) (int, string, bool) {
	index := 0
	for index < len(line) && line[index] == ' ' && index < 4 {
		index++
	}
	if index == 4 || index == len(line) || line[index] != '>' {
		return 0, "", false
	}

	depth := 0
	for index < len(line) && line[index] == '>' {
		depth++
		index++
		if index < len(line) && (line[index] == ' ' || line[index] == '\t') {
			index++
		}
	}
	return depth, line[index:], true
}

func parseMarkdownTable(lines []string, start int) (markdownTable, int, bool) {
	if start+1 >= len(lines) {
		return markdownTable{}, start, false
	}

	header, headerRow := splitMarkdownTableRow(lines[start])
	delimiters, delimiterRow := splitMarkdownTableRow(lines[start+1])
	if !headerRow || !delimiterRow || len(header) == 0 || len(delimiters) != len(header) {
		return markdownTable{}, start, false
	}

	alignments, ok := parseMarkdownTableAlignments(delimiters)
	if !ok {
		return markdownTable{}, start, false
	}

	table := markdownTable{rows: [][]string{header}, alignments: alignments}
	end := start + 2
	for end < len(lines) {
		cells, ok := splitMarkdownTableRow(lines[end])
		if !ok {
			break
		}
		table.rows = append(table.rows, fitMarkdownTableRow(cells, len(header)))
		end++
	}
	return table, end, true
}

func splitMarkdownTableRow(line string) ([]string, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' && indent < 4 {
		indent++
	}
	if indent == 4 || indent < len(line) && line[indent] == '\t' {
		return nil, false
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}

	var cells []string
	var cell strings.Builder
	sawSeparator := false
	lastWasSeparator := false
	for index := 0; index < len(line); {
		switch {
		case line[index] == '\\':
			end := index
			for end < len(line) && line[end] == '\\' {
				end++
			}
			count := end - index
			if end == len(line) || line[end] != '|' {
				cell.WriteString(line[index:end])
				lastWasSeparator = false
				index = end
				continue
			}

			cell.WriteString(strings.Repeat("\\", count/2))
			if count%2 == 1 {
				cell.WriteByte('|')
				lastWasSeparator = false
				index = end + 1
				continue
			}
			index = end
		case line[index] == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			sawSeparator = true
			lastWasSeparator = true
			index++
		default:
			cell.WriteByte(line[index])
			lastWasSeparator = false
			index++
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))

	if !sawSeparator {
		return nil, false
	}
	if line[0] == '|' {
		cells = cells[1:]
	}
	if lastWasSeparator {
		cells = cells[:len(cells)-1]
	}
	return cells, true
}

func parseMarkdownTableAlignments(cells []string) ([]markdownTableAlignment, bool) {
	alignments := make([]markdownTableAlignment, len(cells))
	for index, cell := range cells {
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		cell = strings.TrimPrefix(cell, ":")
		cell = strings.TrimSuffix(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return nil, false
		}

		switch {
		case left && right:
			alignments[index] = markdownTableAlignCenter
		case right:
			alignments[index] = markdownTableAlignRight
		default:
			alignments[index] = markdownTableAlignLeft
		}
	}
	return alignments, true
}

func fitMarkdownTableRow(cells []string, columns int) []string {
	row := make([]string, columns)
	copy(row, cells)
	return row
}

func wrapMarkdownBlock(block markdownBlock, width int) []Line {
	switch block.kind {
	case markdownBlockParagraph, markdownBlockBlank:
		return wrapInlineMarkdown(strings.Join(block.lines, "\n"), width)
	case markdownBlockFencedCode:
		var lines []Line
		for _, sourceLine := range block.lines {
			for index, wrapped := range Wrap(sourceLine, width) {
				lines = append(lines, Line{
					Spans: AppendSpan(nil, wrapped, Style{}), Break: Break{Continuation: index > 0}, FencedCode: true,
				})
			}
		}
		return lines
	case markdownBlockHeading:
		style := Style{Bold: true}
		if block.headingLevel <= 2 {
			style.Foreground = ForegroundAccent
		}
		return WrapSpans(parseInline(block.lines[0], style), width)
	case markdownBlockThematicBreak:
		return []Line{{Spans: []Span{{Text: strings.Repeat("─", width)}}, ThematicBreak: true}}
	case markdownBlockBlockQuote:
		return wrapMarkdownBlockQuote(block.quoteDepth, block.lines[0], width)
	case markdownBlockListItem:
		return wrapMarkdownListItem(block.listPrefix, block.lines[0], width)
	case markdownBlockTable:
		return wrapMarkdownTable(block.table, width)
	default:
		return nil
	}
}

func wrapMarkdownBlockQuote(depth int, content string, width int) []Line {
	prefixStyle := Style{Foreground: ForegroundMuted}
	if content == "" {
		prefix := Truncate(strings.TrimRight(strings.Repeat("│ ", depth), " "), width, false)
		return []Line{{Spans: AppendSpan(nil, prefix, prefixStyle)}}
	}

	prefix := markdownQuotePrefix(depth, width)
	prefixWidth := Width(prefix)
	wrapped := WrapSpans(ParseInline(content), width-prefixWidth)
	for index := range wrapped {
		spans := AppendSpan(nil, prefix, prefixStyle)
		for _, span := range wrapped[index].Spans {
			spans = AppendSpan(spans, span.Text, span.Style)
		}
		wrapped[index].Spans = spans
	}
	return wrapped
}

func markdownQuotePrefix(depth, width int) string {
	return Truncate(strings.Repeat("│ ", depth), max(0, width-1), false)
}

func wrapMarkdownListItem(prefix, content string, width int) []Line {
	contentSpans := ParseInline(content)
	prefixWidth := Width(prefix)
	if prefixWidth >= width {
		spans := []Span{{Text: prefix}}
		for _, span := range contentSpans {
			spans = AppendSpan(spans, span.Text, span.Style)
		}
		return WrapSpans(spans, width)
	}

	wrapped := WrapSpans(contentSpans, width-prefixWidth)
	for index := range wrapped {
		linePrefix := strings.Repeat(" ", prefixWidth)
		if index == 0 {
			linePrefix = prefix
		}
		spans := []Span{{Text: linePrefix}}
		for _, span := range wrapped[index].Spans {
			spans = AppendSpan(spans, span.Text, span.Style)
		}
		wrapped[index].Spans = spans
	}
	return wrapped
}

const markdownTableMinimumColumnWidth = 3

func wrapMarkdownTable(table markdownTable, width int) []Line {
	columns := len(table.alignments)
	separatorWidth := columns - 1
	paddingWidth := columns * 2
	availableWidth := width - separatorWidth - paddingWidth
	if availableWidth < columns*markdownTableMinimumColumnWidth {
		return wrapStackedMarkdownTable(table, width)
	}

	rows := make([][][]Span, len(table.rows))
	preferredWidths := make([]int, columns)
	for rowIndex, row := range table.rows {
		rows[rowIndex] = make([][]Span, columns)
		for column := range columns {
			style := Style{}
			if rowIndex == 0 {
				style.Bold = true
			}
			spans := parseInline(row[column], style)
			rows[rowIndex][column] = spans
			preferredWidths[column] = max(preferredWidths[column], Width(Plain(spans)))
		}
	}
	columnWidths := fitMarkdownTableWidths(preferredWidths, availableWidth)

	lines := wrapMarkdownTableRow(rows[0], table.alignments, columnWidths)
	lines = append(lines, markdownTableRule(columnWidths))
	for _, row := range rows[1:] {
		lines = append(lines, wrapMarkdownTableRow(row, table.alignments, columnWidths)...)
	}
	return lines
}

func fitMarkdownTableWidths(preferred []int, available int) []int {
	widths := make([]int, len(preferred))
	for index := range widths {
		widths[index] = markdownTableMinimumColumnWidth
	}

	remaining := available - len(widths)*markdownTableMinimumColumnWidth
	for remaining > 0 {
		grew := false
		for index := range widths {
			if widths[index] >= preferred[index] {
				continue
			}
			widths[index]++
			remaining--
			grew = true
			if remaining == 0 {
				break
			}
		}
		if !grew {
			break
		}
	}
	return widths
}

func wrapMarkdownTableRow(cells [][]Span, alignments []markdownTableAlignment, widths []int) []Line {
	wrapped := make([][]Line, len(cells))
	height := 1
	for column, spans := range cells {
		wrapped[column] = WrapSpans(spans, widths[column])
		height = max(height, len(wrapped[column]))
	}

	lines := make([]Line, 0, height)
	for rowLine := range height {
		var spans []Span
		for column := range cells {
			if column > 0 {
				spans = AppendSpan(spans, "│", Style{})
			}
			spans = AppendSpan(spans, " ", Style{})

			var content []Span
			if rowLine < len(wrapped[column]) {
				content = wrapped[column][rowLine].Spans
			}
			contentWidth := Width(Plain(content))
			left, right := markdownTableCellPadding(widths[column]-contentWidth, alignments[column])
			spans = AppendSpan(spans, strings.Repeat(" ", left), Style{})
			for _, span := range content {
				spans = AppendSpan(spans, span.Text, span.Style)
			}
			spans = AppendSpan(spans, strings.Repeat(" ", right+1), Style{})
		}
		lines = append(lines, Line{Spans: spans, Break: Break{Continuation: rowLine > 0}})
	}
	return lines
}

func markdownTableCellPadding(space int, alignment markdownTableAlignment) (int, int) {
	switch alignment {
	case markdownTableAlignCenter:
		return space / 2, space - space/2
	case markdownTableAlignRight:
		return space, 0
	default:
		return 0, space
	}
}

func markdownTableRule(widths []int) Line {
	parts := make([]string, len(widths))
	for index, width := range widths {
		parts[index] = strings.Repeat("─", width+2)
	}
	return Line{Spans: []Span{{Text: strings.Join(parts, "┼")}}}
}

func wrapStackedMarkdownTable(table markdownTable, width int) []Line {
	if len(table.rows) == 1 {
		var lines []Line
		for _, heading := range table.rows[0] {
			lines = append(lines, WrapSpans(parseInline(heading, Style{Bold: true}), width)...)
		}
		return lines
	}

	headings := make([][]Span, len(table.rows[0]))
	headingStyle := Style{Bold: true}
	for column, heading := range table.rows[0] {
		headings[column] = AppendSpan(parseInline(heading, headingStyle), ":", headingStyle)
	}

	var lines []Line
	for rowIndex, row := range table.rows[1:] {
		if rowIndex > 0 {
			lines = append(lines, Line{})
		}
		for column, value := range row {
			spans := append([]Span(nil), headings[column]...)
			spans = AppendSpan(spans, " ", Style{})
			for _, span := range ParseInline(value) {
				spans = AppendSpan(spans, span.Text, span.Style)
			}
			lines = append(lines, WrapSpans(spans, width)...)
		}
	}
	return lines
}

func wrapInlineMarkdown(source string, width int) []Line {
	return WrapSpans(ParseInline(source), width)
}
