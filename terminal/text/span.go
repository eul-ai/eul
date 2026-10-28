package text

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Foreground uint8

const (
	ForegroundDefault Foreground = iota
	ForegroundAccent
	ForegroundMuted
	ForegroundOrange
	ForegroundSuccess
	ForegroundError
)

type Style struct {
	Bold       bool
	Italic     bool
	Code       bool
	Foreground Foreground
	Link       string
}

type Span struct {
	Text   string
	Style  Style
	Atomic bool
}

type Break struct {
	Continuation bool
	Separator    string
}

func (b Break) SourceSeparator() string {
	if b.Continuation {
		return b.Separator
	}
	return "\n"
}

type Line struct {
	Spans         []Span
	Break         Break
	FencedCode    bool
	ThematicBreak bool
}

func (line Line) Text() string {
	return Plain(line.Spans)
}

func AppendSpan(spans []Span, value string, style Style) []Span {
	if value == "" {
		return spans
	}
	if len(spans) > 0 && !spans[len(spans)-1].Atomic && spans[len(spans)-1].Style == style {
		spans[len(spans)-1].Text += value
		return spans
	}
	return append(spans, Span{Text: value, Style: style})
}

func Plain(spans []Span) string {
	switch len(spans) {
	case 0:
		return ""
	case 1:
		return spans[0].Text
	}

	var plain strings.Builder
	for _, span := range spans {
		plain.WriteString(span.Text)
	}
	return plain.String()
}

func TruncateSpans(spans []Span, width int) []Span {
	if width <= 0 {
		return nil
	}

	var result []Span
	used := 0
	for _, span := range spans {
		end := 0
		valid := true
		for end < len(span.Text) {
			character, size := utf8.DecodeRuneInString(span.Text[end:])
			characterWidth := RuneWidth(character)
			if used+characterWidth > width {
				break
			}
			if character == utf8.RuneError && size == 1 {
				valid = false
			}
			used += characterWidth
			end += size
		}
		value := span.Text[:end]
		if !valid {
			value = normalizeRunes(value)
		}
		result = AppendSpan(result, value, span.Style)
		if end < len(span.Text) {
			break
		}
	}
	return result
}

type wrapToken struct {
	spans      []Span
	width      int
	whitespace bool
	atomic     bool
	newline    bool
}

func WrapSpans(spans []Span, width int) []Line {
	if width <= 0 {
		return nil
	}

	lines := make([]Line, 0, 1)
	var pieces []Span
	lineWidth := 0
	breakBefore := Break{}
	appendPiece := func(value string, style Style) {
		if value != "" {
			pieces = append(pieces, Span{Text: value, Style: style})
		}
	}
	flush := func() {
		lines = append(lines, Line{Spans: joinSpans(pieces), Break: breakBefore})
		pieces = pieces[:0]
		lineWidth = 0
	}
	startContinuation := func(separator string) {
		breakBefore = Break{Continuation: true, Separator: separator}
	}
	appendSpans := func(spans []Span) {
		for _, span := range spans {
			appendPiece(span.Text, span.Style)
		}
	}
	appendHard := func(spans []Span) {
		for _, span := range spans {
			start := 0
			for index := 0; index < len(span.Text); {
				character, size := utf8.DecodeRuneInString(span.Text[index:])
				characterWidth := RuneWidth(character)
				if lineWidth > 0 && lineWidth+characterWidth > width {
					appendPiece(span.Text[start:index], span.Style)
					flush()
					startContinuation("")
					start = index
				}
				lineWidth += characterWidth
				index += size
			}
			appendPiece(span.Text[start:], span.Style)
		}
	}

	var pendingWhitespace []Span
	pendingWidth := 0
	clearPendingWhitespace := func() {
		clear(pendingWhitespace)
		pendingWhitespace = pendingWhitespace[:0]
		pendingWidth = 0
	}
	appendToken := func(token wrapToken) {
		if token.newline {
			appendHard(pendingWhitespace)
			clearPendingWhitespace()
			flush()
			breakBefore = Break{}
			return
		}
		if token.whitespace {
			for _, span := range token.spans {
				pendingWhitespace = AppendSpan(pendingWhitespace, span.Text, span.Style)
			}
			pendingWidth += token.width
			return
		}

		if lineWidth == 0 {
			appendHard(pendingWhitespace)
			clearPendingWhitespace()
		}
		if lineWidth > 0 && len(pendingWhitespace) > 0 {
			if lineWidth+pendingWidth+token.width <= width {
				appendSpans(pendingWhitespace)
				lineWidth += pendingWidth
				clearPendingWhitespace()
			} else {
				separator := Plain(pendingWhitespace)
				clearPendingWhitespace()
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
				appendPiece(Truncate(span.Text, width, false), span.Style)
				lineWidth = width
				return
			}
			appendSpans(token.spans)
			lineWidth += token.width
			return
		}
		appendHard(token.spans)
	}
	walkWrapTokens(spans, appendToken)

	appendHard(pendingWhitespace)
	flush()
	return lines
}

func joinSpans(pieces []Span) []Span {
	var joined []Span
	for start := 0; start < len(pieces); {
		end := start + 1
		for end < len(pieces) && pieces[end].Style == pieces[start].Style {
			end++
		}
		if end-start == 1 {
			joined = append(joined, pieces[start])
			start = end
			continue
		}
		var value strings.Builder
		for _, piece := range pieces[start:end] {
			value.WriteString(piece.Text)
		}
		joined = append(joined, Span{Text: value.String(), Style: pieces[start].Style})
		start = end
	}
	return joined
}

func walkWrapTokens(spans []Span, yield func(wrapToken)) {
	var token wrapToken
	flush := func() {
		if len(token.spans) == 0 {
			return
		}
		yield(token)
		clear(token.spans)
		token.spans = token.spans[:0]
		token.width = 0
	}
	appendText := func(value string, width int, style Style, whitespace bool) {
		if len(token.spans) > 0 && token.whitespace != whitespace {
			flush()
		}
		token.whitespace = whitespace
		token.spans = AppendSpan(token.spans, value, style)
		token.width += width
	}

	for _, span := range spans {
		if span.Atomic {
			flush()
			yield(wrapToken{
				spans:  []Span{{Text: span.Text, Style: span.Style}},
				width:  Width(span.Text),
				atomic: true,
			})
			continue
		}

		for index := 0; index < len(span.Text); {
			character, size := utf8.DecodeRuneInString(span.Text[index:])
			if character == '\n' {
				flush()
				yield(wrapToken{newline: true})
				index += size
				continue
			}

			start := index
			whitespace := unicode.IsSpace(character)
			runWidth := 0
			for index < len(span.Text) {
				character, size = utf8.DecodeRuneInString(span.Text[index:])
				if character == '\n' || unicode.IsSpace(character) != whitespace {
					break
				}
				if character == '\t' {
					runWidth += 4
				} else {
					runWidth += RuneWidth(character)
				}
				index += size
			}
			appendText(normalizeWrapRun(span.Text[start:index]), runWidth, span.Style, whitespace)
		}
	}
	flush()
}

func normalizeRunes(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range value {
		normalized.WriteRune(character)
	}
	return normalized.String()
}

func normalizeWrapRun(value string) string {
	if !strings.ContainsRune(value, '\t') && utf8.ValidString(value) {
		return value
	}

	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range value {
		if character == '\t' {
			normalized.WriteString("    ")
		} else {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func ParseInline(source string) []Span {
	return parseInline(source, Style{})
}

func parseInline(source string, inherited Style) []Span {
	var spans []Span
	plainStart := 0
	appendPlain := func(end int) {
		spans = AppendSpan(spans, source[plainStart:end], inherited)
		plainStart = end
	}

	for index := 0; index < len(source); {
		if inherited.Link == "" {
			if label, destination, consumed, ok := parseLink(source[index:]); ok {
				appendPlain(index)
				style := inherited
				style.Link = destination
				for _, span := range parseInline(label, style) {
					spans = AppendSpan(spans, span.Text, span.Style)
				}
				index += consumed
				plainStart = index
				continue
			}
			if destination, consumed, ok := parseAutolink(source[index:]); ok {
				appendPlain(index)
				style := inherited
				style.Link = destination
				spans = AppendSpan(spans, destination, style)
				index += consumed
				plainStart = index
				continue
			}
		}

		delimiter := ""
		style := Style{}
		switch {
		case source[index] == '`':
			end := index
			for end < len(source) && source[end] == '`' {
				end++
			}
			if end-index >= 3 {
				index = end
				continue
			}
			delimiter = source[index:end]
			style = Style{Code: true}
		case strings.HasPrefix(source[index:], "***"):
			delimiter = "***"
			style = Style{Bold: true, Italic: true}
		case strings.HasPrefix(source[index:], "**"):
			delimiter = "**"
			style = Style{Bold: true}
		case source[index] == '*':
			delimiter = "*"
			style = Style{Italic: true}
		}
		if delimiter != "" {
			contentStart := index + len(delimiter)
			closing := strings.Index(source[contentStart:], delimiter)
			if closing > 0 {
				appendPlain(index)
				contentEnd := contentStart + closing
				content := source[contentStart:contentEnd]
				if style.Code {
					spans = AppendSpan(spans, content, mergeStyles(inherited, style))
				} else {
					for _, span := range parseInline(content, mergeStyles(inherited, style)) {
						spans = AppendSpan(spans, span.Text, span.Style)
					}
				}
				index = contentEnd + len(delimiter)
				plainStart = index
				continue
			}
			index += len(delimiter)
			continue
		}

		_, size := utf8.DecodeRuneInString(source[index:])
		index += size
	}
	appendPlain(len(source))
	return spans
}

func parseLink(source string) (string, string, int, bool) {
	if !strings.HasPrefix(source, "[") {
		return "", "", 0, false
	}
	labelEnd := strings.Index(source, "](")
	if labelEnd <= 1 {
		return "", "", 0, false
	}
	destinationStart := labelEnd + 2
	destinationEnd := linkDestinationEnd(source, destinationStart)
	if destinationEnd <= destinationStart {
		return "", "", 0, false
	}
	destination := source[destinationStart:destinationEnd]
	if !clickableURL(destination) {
		return "", "", 0, false
	}
	return source[1:labelEnd], destination, destinationEnd + 1, true
}

func linkDestinationEnd(source string, start int) int {
	depth := 0
	for index := start; index < len(source); index++ {
		switch source[index] {
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

func parseAutolink(source string) (string, int, bool) {
	if strings.HasPrefix(source, "<") {
		end := strings.IndexByte(source, '>')
		if end < 0 || !clickableURL(source[1:end]) {
			return "", 0, false
		}
		return source[1:end], end + 1, true
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return "", 0, false
	}

	end := 0
	for end < len(source) {
		character, size := utf8.DecodeRuneInString(source[end:])
		if unicode.IsSpace(character) {
			break
		}
		end += size
	}
	end = bareURLDestinationEnd(source, end)
	return source[:end], end, true
}

func bareURLDestinationEnd(source string, end int) int {
	for end > 0 {
		character := rune(source[end-1])
		switch {
		case strings.ContainsRune(".,;:!?]}>\"'*", character):
			end--
		case character == ')':
			if strings.Count(source[:end], ")") <= strings.Count(source[:end], "(") {
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

func mergeStyles(left, right Style) Style {
	foreground := left.Foreground
	if right.Foreground != ForegroundDefault {
		foreground = right.Foreground
	}
	link := left.Link
	if right.Link != "" {
		link = right.Link
	}
	return Style{
		Bold:       left.Bold || right.Bold,
		Italic:     left.Italic || right.Italic,
		Code:       left.Code || right.Code,
		Foreground: foreground,
		Link:       link,
	}
}
