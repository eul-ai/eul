package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type inlineForeground uint8

const (
	inlineForegroundDefault inlineForeground = iota
	inlineForegroundAccent
	inlineForegroundMuted
	inlineForegroundOrange
	inlineForegroundSuccess
	inlineForegroundError
)

type inlineStyle struct {
	bold       bool
	italic     bool
	code       bool
	foreground inlineForeground
	link       string
}

type inlineSpan struct {
	text   string
	style  inlineStyle
	atomic bool
}

type formattedLine struct {
	text          string
	spans         []inlineSpan
	breakBefore   lineBreak
	fencedCode    bool
	thematicBreak bool
}

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

func wrapMarkdown(text string, width int) []formattedLine {
	if width <= 0 {
		return nil
	}

	var lines []formattedLine
	for _, block := range parseMarkdownBlocks(text) {
		lines = append(lines, wrapMarkdownBlock(block, width)...)
	}
	return lines
}

func parseMarkdownBlocks(text string) []markdownBlock {
	sourceLines := strings.Split(text, "\n")
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
			blocks = append(blocks, markdownBlock{kind: markdownBlockFencedCode, lines: sourceLines[start:index]})
			if index < len(sourceLines) {
				index++
			}
		case line == "":
			start := index
			for index < len(sourceLines) && sourceLines[index] == "" {
				index++
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockBlank, lines: sourceLines[start:index]})
		default:
			if level, content, ok := parseMarkdownHeading(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockHeading, lines: []string{content}, headingLevel: level,
				})
				index++
				continue
			}
			if isMarkdownThematicBreak(line) {
				blocks = append(blocks, markdownBlock{kind: markdownBlockThematicBreak})
				index++
				continue
			}
			if depth, content, ok := parseMarkdownBlockQuote(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockBlockQuote, lines: []string{content}, quoteDepth: depth,
				})
				index++
				continue
			}
			if table, end, ok := parseMarkdownTable(sourceLines, index); ok {
				blocks = append(blocks, markdownBlock{kind: markdownBlockTable, table: table})
				index = end
				continue
			}
			if prefix, content, ok := parseMarkdownListItem(line); ok {
				blocks = append(blocks, markdownBlock{
					kind: markdownBlockListItem, lines: []string{content}, listPrefix: prefix,
				})
				index++
				continue
			}

			start := index
			for index < len(sourceLines) && !startsMarkdownBlock(sourceLines, index) {
				index++
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockParagraph, lines: sourceLines[start:index]})
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

func wrapMarkdownBlock(block markdownBlock, width int) []formattedLine {
	switch block.kind {
	case markdownBlockParagraph, markdownBlockBlank:
		return wrapInlineMarkdown(strings.Join(block.lines, "\n"), width)
	case markdownBlockFencedCode:
		var lines []formattedLine
		for _, sourceLine := range block.lines {
			for index, wrapped := range wrapText(sourceLine, width) {
				lines = append(lines, formattedLine{
					text: wrapped, breakBefore: lineBreak{continuation: index > 0}, fencedCode: true,
				})
			}
		}
		return lines
	case markdownBlockHeading:
		style := inlineStyle{bold: true}
		if block.headingLevel <= 2 {
			style.foreground = inlineForegroundAccent
		}
		return wrapInlineSpans(parseInlineMarkdownStyle(block.lines[0], style), width)
	case markdownBlockThematicBreak:
		return []formattedLine{{text: strings.Repeat("─", width), thematicBreak: true}}
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

func wrapMarkdownBlockQuote(depth int, content string, width int) []formattedLine {
	prefixStyle := inlineStyle{foreground: inlineForegroundMuted}
	prefix := strings.Repeat("│ ", depth)
	if content == "" {
		prefix = truncateCells(strings.TrimRight(prefix, " "), width, false)
		return []formattedLine{{text: prefix, spans: []inlineSpan{{text: prefix, style: prefixStyle}}}}
	}

	prefix = truncateCells(prefix, max(0, width-1), false)
	prefixWidth := cellWidth(prefix)
	wrapped := wrapInlineSpans(parseInlineMarkdown(content), width-prefixWidth)
	for index := range wrapped {
		var spans []inlineSpan
		appendInlineSpan(&spans, prefix, prefixStyle)
		for _, span := range wrapped[index].spans {
			appendInlineSpan(&spans, span.text, span.style)
		}
		wrapped[index].text = inlineSpanText(spans)
		wrapped[index].spans = spans
	}
	return wrapped
}

func wrapMarkdownListItem(prefix, content string, width int) []formattedLine {
	contentSpans := parseInlineMarkdown(content)
	prefixWidth := cellWidth(prefix)
	if prefixWidth >= width {
		spans := []inlineSpan{{text: prefix}}
		for _, span := range contentSpans {
			appendInlineSpan(&spans, span.text, span.style)
		}
		return wrapInlineSpans(spans, width)
	}

	wrapped := wrapInlineSpans(contentSpans, width-prefixWidth)
	for index := range wrapped {
		linePrefix := strings.Repeat(" ", prefixWidth)
		if index == 0 {
			linePrefix = prefix
		}
		spans := []inlineSpan{{text: linePrefix}}
		for _, span := range wrapped[index].spans {
			appendInlineSpan(&spans, span.text, span.style)
		}
		wrapped[index].text = inlineSpanText(spans)
		wrapped[index].spans = spans
	}
	return wrapped
}

const markdownTableMinimumColumnWidth = 3

func wrapMarkdownTable(table markdownTable, width int) []formattedLine {
	columns := len(table.alignments)
	separatorWidth := columns - 1
	paddingWidth := columns * 2
	availableWidth := width - separatorWidth - paddingWidth
	if availableWidth < columns*markdownTableMinimumColumnWidth {
		return wrapStackedMarkdownTable(table, width)
	}

	rows := make([][][]inlineSpan, len(table.rows))
	preferredWidths := make([]int, columns)
	for rowIndex, row := range table.rows {
		rows[rowIndex] = make([][]inlineSpan, columns)
		for column := range columns {
			style := inlineStyle{}
			if rowIndex == 0 {
				style.bold = true
			}
			spans := parseInlineMarkdownStyle(row[column], style)
			rows[rowIndex][column] = spans
			preferredWidths[column] = max(preferredWidths[column], cellWidth(inlineSpanText(spans)))
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

func wrapMarkdownTableRow(cells [][]inlineSpan, alignments []markdownTableAlignment, widths []int) []formattedLine {
	wrapped := make([][]formattedLine, len(cells))
	height := 1
	for column, spans := range cells {
		wrapped[column] = wrapInlineSpans(spans, widths[column])
		height = max(height, len(wrapped[column]))
	}

	lines := make([]formattedLine, 0, height)
	for rowLine := range height {
		var spans []inlineSpan
		for column := range cells {
			if column > 0 {
				appendInlineSpan(&spans, "│", inlineStyle{})
			}
			appendInlineSpan(&spans, " ", inlineStyle{})

			var content []inlineSpan
			if rowLine < len(wrapped[column]) {
				content = wrapped[column][rowLine].spans
			}
			contentWidth := cellWidth(inlineSpanText(content))
			left, right := markdownTableCellPadding(widths[column]-contentWidth, alignments[column])
			appendInlineSpan(&spans, strings.Repeat(" ", left), inlineStyle{})
			for _, span := range content {
				appendInlineSpan(&spans, span.text, span.style)
			}
			appendInlineSpan(&spans, strings.Repeat(" ", right+1), inlineStyle{})
		}
		lines = append(lines, formattedLine{
			text: inlineSpanText(spans), spans: spans,
			breakBefore: lineBreak{continuation: rowLine > 0},
		})
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

func markdownTableRule(widths []int) formattedLine {
	parts := make([]string, len(widths))
	for index, width := range widths {
		parts[index] = strings.Repeat("─", width+2)
	}
	return formattedLine{text: strings.Join(parts, "┼")}
}

func wrapStackedMarkdownTable(table markdownTable, width int) []formattedLine {
	if len(table.rows) == 1 {
		var lines []formattedLine
		for _, heading := range table.rows[0] {
			lines = append(lines, wrapInlineSpans(parseInlineMarkdownStyle(heading, inlineStyle{bold: true}), width)...)
		}
		return lines
	}

	headings := make([][]inlineSpan, len(table.rows[0]))
	headingStyle := inlineStyle{bold: true}
	for column, heading := range table.rows[0] {
		headings[column] = parseInlineMarkdownStyle(heading, headingStyle)
		appendInlineSpan(&headings[column], ":", headingStyle)
	}

	var lines []formattedLine
	for rowIndex, row := range table.rows[1:] {
		if rowIndex > 0 {
			lines = append(lines, formattedLine{})
		}
		for column, value := range row {
			spans := append([]inlineSpan(nil), headings[column]...)
			appendInlineSpan(&spans, " ", inlineStyle{})
			for _, span := range parseInlineMarkdown(value) {
				appendInlineSpan(&spans, span.text, span.style)
			}
			lines = append(lines, wrapInlineSpans(spans, width)...)
		}
	}
	return lines
}

func wrapInlineMarkdown(text string, width int) []formattedLine {
	return wrapInlineSpans(parseInlineMarkdown(text), width)
}

type inlineWrapToken struct {
	spans      []inlineSpan
	width      int
	whitespace bool
	atomic     bool
	newline    bool
}

func wrapInlineSpans(spans []inlineSpan, width int) []formattedLine {
	if width <= 0 {
		return nil
	}

	lines := make([]formattedLine, 0, 1)
	current := make([]inlineSpan, 0, 1)
	lineWidth := 0
	breakBefore := lineBreak{}
	flush := func() {
		lines = append(lines, formattedLine{
			text:        inlineSpanText(current),
			spans:       current,
			breakBefore: breakBefore,
		})
		current = nil
		lineWidth = 0
	}
	startContinuation := func(separator string) {
		breakBefore = lineBreak{continuation: true, separator: separator}
	}
	appendSpans := func(spans []inlineSpan) {
		for _, span := range spans {
			appendInlineSpan(&current, span.text, span.style)
		}
	}
	appendHard := func(spans []inlineSpan) {
		for _, span := range spans {
			for _, character := range span.text {
				characterWidth := runeWidth(character)
				if lineWidth > 0 && lineWidth+characterWidth > width {
					flush()
					startContinuation("")
				}
				appendInlineSpan(&current, string(character), span.style)
				lineWidth += characterWidth
			}
		}
	}

	var pendingWhitespace []inlineSpan
	pendingWidth := 0
	for _, token := range inlineWrapTokens(spans) {
		if token.newline {
			appendHard(pendingWhitespace)
			pendingWhitespace = nil
			pendingWidth = 0
			flush()
			breakBefore = lineBreak{}
			continue
		}
		if token.whitespace {
			for _, span := range token.spans {
				appendInlineSpan(&pendingWhitespace, span.text, span.style)
			}
			pendingWidth += token.width
			continue
		}

		if lineWidth == 0 {
			appendHard(pendingWhitespace)
			pendingWhitespace = nil
			pendingWidth = 0
		}
		if lineWidth > 0 && len(pendingWhitespace) > 0 {
			if lineWidth+pendingWidth+token.width <= width {
				appendSpans(pendingWhitespace)
				lineWidth += pendingWidth
				pendingWhitespace = nil
				pendingWidth = 0
			} else {
				separator := inlineSpanText(pendingWhitespace)
				pendingWhitespace = nil
				pendingWidth = 0
				flush()
				startContinuation(separator)
			}
		}

		if token.atomic && lineWidth > 0 && lineWidth+token.width > width {
			flush()
			startContinuation("")
		}
		if token.atomic {
			span := token.spans[0]
			if token.width > width {
				appendInlineSpan(&current, truncateCells(span.text, width, false), span.style)
				lineWidth = width
				continue
			}
			appendSpans(token.spans)
			lineWidth += token.width
			continue
		}
		appendHard(token.spans)
	}

	appendHard(pendingWhitespace)
	flush()
	return lines
}

func inlineWrapTokens(spans []inlineSpan) []inlineWrapToken {
	var tokens []inlineWrapToken
	appendText := func(text string, style inlineStyle, whitespace bool) {
		if len(tokens) == 0 || tokens[len(tokens)-1].whitespace != whitespace || tokens[len(tokens)-1].atomic || tokens[len(tokens)-1].newline {
			tokens = append(tokens, inlineWrapToken{whitespace: whitespace})
		}
		token := &tokens[len(tokens)-1]
		appendInlineSpan(&token.spans, text, style)
		token.width += cellWidth(text)
	}

	for _, span := range spans {
		if span.atomic {
			tokens = append(tokens, inlineWrapToken{
				spans:  []inlineSpan{{text: span.text, style: span.style}},
				width:  cellWidth(span.text),
				atomic: true,
			})
			continue
		}
		for _, character := range span.text {
			switch {
			case character == '\n':
				tokens = append(tokens, inlineWrapToken{newline: true})
			case character == '\t':
				appendText("    ", span.style, true)
			default:
				appendText(string(character), span.style, unicode.IsSpace(character))
			}
		}
	}
	return tokens
}

func parseInlineMarkdown(text string) []inlineSpan {
	return parseInlineMarkdownStyle(text, inlineStyle{})
}

func parseInlineMarkdownStyle(text string, inherited inlineStyle) []inlineSpan {
	var spans []inlineSpan
	for index := 0; index < len(text); {
		if inherited.link == "" {
			if label, destination, consumed, ok := parseMarkdownLink(text[index:]); ok {
				style := inherited
				style.link = destination
				for _, span := range parseInlineMarkdownStyle(label, style) {
					appendInlineSpan(&spans, span.text, span.style)
				}
				index += consumed
				continue
			}
			if destination, consumed, ok := parseAutolink(text[index:]); ok {
				style := inherited
				style.link = destination
				appendInlineSpan(&spans, destination, style)
				index += consumed
				continue
			}
		}

		delimiter := ""
		style := inlineStyle{}
		switch {
		case text[index] == '`':
			end := index
			for end < len(text) && text[end] == '`' {
				end++
			}
			if end-index >= 3 {
				appendInlineSpan(&spans, text[index:end], inherited)
				index = end
				continue
			}
			delimiter = text[index:end]
			style = inlineStyle{code: true}
		case strings.HasPrefix(text[index:], "***"):
			delimiter = "***"
			style = inlineStyle{bold: true, italic: true}
		case strings.HasPrefix(text[index:], "**"):
			delimiter = "**"
			style = inlineStyle{bold: true}
		case text[index] == '*':
			delimiter = "*"
			style = inlineStyle{italic: true}
		}
		if delimiter != "" {
			contentStart := index + len(delimiter)
			closing := strings.Index(text[contentStart:], delimiter)
			if closing > 0 {
				contentEnd := contentStart + closing
				content := text[contentStart:contentEnd]
				if style.code {
					appendInlineSpan(&spans, content, mergeInlineStyles(inherited, style))
				} else {
					for _, span := range parseInlineMarkdownStyle(content, mergeInlineStyles(inherited, style)) {
						appendInlineSpan(&spans, span.text, span.style)
					}
				}
				index = contentEnd + len(delimiter)
				continue
			}
			appendInlineSpan(&spans, delimiter, inherited)
			index += len(delimiter)
			continue
		}

		_, size := utf8.DecodeRuneInString(text[index:])
		appendInlineSpan(&spans, text[index:index+size], inherited)
		index += size
	}
	return spans
}

func parseMarkdownLink(text string) (string, string, int, bool) {
	if !strings.HasPrefix(text, "[") {
		return "", "", 0, false
	}
	labelEnd := strings.Index(text, "](")
	if labelEnd <= 1 {
		return "", "", 0, false
	}
	destinationStart := labelEnd + 2
	destinationEnd := markdownDestinationEnd(text, destinationStart)
	if destinationEnd <= destinationStart {
		return "", "", 0, false
	}
	destination := text[destinationStart:destinationEnd]
	if !clickableURL(destination) {
		return "", "", 0, false
	}
	return text[1:labelEnd], destination, destinationEnd + 1, true
}

func markdownDestinationEnd(text string, start int) int {
	depth := 0
	for index := start; index < len(text); index++ {
		switch text[index] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return index
			}
			depth--
		}
	}
	return -1
}

func parseAutolink(text string) (string, int, bool) {
	if strings.HasPrefix(text, "<") {
		end := strings.IndexByte(text, '>')
		if end < 0 || !clickableURL(text[1:end]) {
			return "", 0, false
		}
		return text[1:end], end + 1, true
	}
	if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		return "", 0, false
	}

	end := 0
	for end < len(text) {
		character, size := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(character) {
			break
		}
		end += size
	}
	end = bareURLDestinationEnd(text, end)
	return text[:end], end, true
}

func bareURLDestinationEnd(text string, end int) int {
	for end > 0 {
		character := rune(text[end-1])
		switch {
		case strings.ContainsRune(".,;:!?]}>\"'*", character):
			end--
		case character == ')':
			if strings.Count(text[:end], ")") <= strings.Count(text[:end], "(") {
				return end
			}
			end--
		default:
			return end
		}
	}
	return end
}

func clickableURL(destination string) bool {
	if strings.ContainsFunc(destination, unicode.IsSpace) {
		return false
	}
	return strings.HasPrefix(destination, "http://") || strings.HasPrefix(destination, "https://") || strings.HasPrefix(destination, "mailto:")
}

func mergeInlineStyles(left, right inlineStyle) inlineStyle {
	foreground := left.foreground
	if right.foreground != inlineForegroundDefault {
		foreground = right.foreground
	}
	link := left.link
	if right.link != "" {
		link = right.link
	}
	return inlineStyle{
		bold:       left.bold || right.bold,
		italic:     left.italic || right.italic,
		code:       left.code || right.code,
		foreground: foreground,
		link:       link,
	}
}

func truncateInlineSpans(spans []inlineSpan, width int) []inlineSpan {
	if width <= 0 {
		return nil
	}

	var result []inlineSpan
	used := 0
truncate:
	for _, span := range spans {
		for _, character := range span.text {
			characterWidth := runeWidth(character)
			if used+characterWidth > width {
				break truncate
			}
			appendInlineSpan(&result, string(character), span.style)
			used += characterWidth
		}
	}
	return result
}

func appendInlineSpan(spans *[]inlineSpan, text string, style inlineStyle) {
	if text == "" {
		return
	}
	if len(*spans) > 0 && !(*spans)[len(*spans)-1].atomic && (*spans)[len(*spans)-1].style == style {
		(*spans)[len(*spans)-1].text += text
		return
	}
	*spans = append(*spans, inlineSpan{text: text, style: style})
}

func inlineSpanText(spans []inlineSpan) string {
	var text strings.Builder
	for _, span := range spans {
		text.WriteString(span.text)
	}
	return text.String()
}
