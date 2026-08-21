package terminal

import (
	"os"
	"reflect"
	"testing"
)

func TestMarkdownFixtureStructure(t *testing.T) {
	source, err := os.ReadFile("testdata/markdown.md")
	if err != nil {
		t.Fatal(err)
	}

	type documentStructure struct {
		blocks          int
		paragraphs      int
		blankLines      int
		headingLevels   []int
		listPrefixes    []string
		quoteDepths     []int
		emptyQuotes     int
		thematicBreaks  int
		tableRows       []int
		tableColumns    []int
		tableAlignments []markdownTableAlignment
	}

	blocks := parseMarkdownBlocks(string(source))
	got := documentStructure{blocks: len(blocks)}
	for _, block := range blocks {
		switch block.kind {
		case markdownBlockParagraph:
			got.paragraphs++
		case markdownBlockBlank:
			got.blankLines += len(block.lines)
		case markdownBlockHeading:
			got.headingLevels = append(got.headingLevels, block.headingLevel)
		case markdownBlockThematicBreak:
			got.thematicBreaks++
		case markdownBlockBlockQuote:
			got.quoteDepths = append(got.quoteDepths, block.quoteDepth)
			if block.lines[0] == "" {
				got.emptyQuotes++
			}
		case markdownBlockListItem:
			got.listPrefixes = append(got.listPrefixes, block.listPrefix)
		case markdownBlockTable:
			got.tableRows = append(got.tableRows, len(block.table.rows))
			got.tableColumns = append(got.tableColumns, len(block.table.alignments))
			got.tableAlignments = append(got.tableAlignments, block.table.alignments...)
		case markdownBlockFencedCode:
			t.Fatal("fixture unexpectedly contains fenced code")
		}
	}

	want := documentStructure{
		blocks:         59,
		paragraphs:     4,
		blankLines:     20,
		headingLevels:  []int{1, 2, 1, 2, 3, 4, 5, 6, 3, 2, 2, 2, 2},
		listPrefixes:   []string{"- ", "- ", "  - ", "  - ", "- ", "1. ", "2. ", "   1. ", "   2. ", "3. ", "- ", "- "},
		quoteDepths:    []int{1, 1, 1, 1, 1, 2},
		emptyQuotes:    2,
		thematicBreaks: 3,
		tableRows:      []int{4},
		tableColumns:   []int{3},
		tableAlignments: []markdownTableAlignment{
			markdownTableAlignLeft,
			markdownTableAlignCenter,
			markdownTableAlignRight,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("structure = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownHeadings(t *testing.T) {
	got := wrapMarkdown("# Main *title*\n### Detail `code`\nparagraph", 80)
	want := []formattedLine{
		{text: "Main title", spans: []inlineSpan{
			{text: "Main ", style: inlineStyle{bold: true, foreground: inlineForegroundAccent}},
			{text: "title", style: inlineStyle{bold: true, italic: true, foreground: inlineForegroundAccent}},
		}},
		{text: "Detail code", spans: []inlineSpan{
			{text: "Detail ", style: inlineStyle{bold: true}},
			{text: "code", style: inlineStyle{bold: true, code: true}},
		}},
		{text: "paragraph", spans: []inlineSpan{{text: "paragraph", style: inlineStyle{}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownHeadingMarkers(t *testing.T) {
	got := wrapMarkdown("   ## indented\n####### plain\n###plain", 80)
	if len(got) != 3 {
		t.Fatalf("lines = %+v", got)
	}
	if got[0].text != "indented" || len(got[0].spans) != 1 || got[0].spans[0].style != (inlineStyle{bold: true, foreground: inlineForegroundAccent}) {
		t.Fatalf("heading = %+v", got[0])
	}
	if got[1].text != "####### plain" || got[2].text != "###plain" {
		t.Fatalf("non-headings = %+v", got[1:])
	}
}

func TestWrapMarkdownListItemsUseHangingIndent(t *testing.T) {
	got := wrapMarkdown("- **alpha beta**\n10. gamma delta\n  - child next", 9)
	wantText := []string{
		"- alpha",
		"  beta",
		"10. gamma",
		"    delta",
		"  - child",
		"    next",
	}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].text != want {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
		if index%2 == 1 && (!got[index].breakBefore.continuation || got[index].breakBefore.separator != " ") {
			t.Fatalf("line %d lost continuation metadata: %+v", index, got[index])
		}
	}
	betaBold := false
	for _, span := range got[1].spans {
		betaBold = betaBold || span.text == "beta" && span.style.bold
	}
	if !betaBold {
		t.Fatalf("wrapped item lost inline style: %+v", got[1])
	}
}

func TestParseMarkdownListItemMarkers(t *testing.T) {
	tests := []struct {
		input       string
		wantPrefix  string
		wantContent string
		wantOK      bool
	}{
		{input: "- item", wantPrefix: "- ", wantContent: "item", wantOK: true},
		{input: "  + nested", wantPrefix: "  + ", wantContent: "nested", wantOK: true},
		{input: "12. ordered", wantPrefix: "12. ", wantContent: "ordered", wantOK: true},
		{input: "-not an item"},
		{input: "12.not an item"},
		{input: "    - indented code"},
		{input: "- - -"},
		{input: "* * *"},
	}
	for _, test := range tests {
		prefix, content, ok := parseMarkdownListItem(test.input)
		if prefix != test.wantPrefix || content != test.wantContent || ok != test.wantOK {
			t.Errorf("parseMarkdownListItem(%q) = %q, %q, %t", test.input, prefix, content, ok)
		}
	}
}

func TestWrapMarkdownThematicBreaks(t *testing.T) {
	got := wrapMarkdown("before\n---\n * * * \n  ___\nafter", 8)
	if len(got) != 5 || got[0].text != "before" || got[4].text != "after" {
		t.Fatalf("lines = %+v", got)
	}
	for index := 1; index <= 3; index++ {
		if got[index].text != "────────" || !got[index].thematicBreak {
			t.Fatalf("line %d = %+v", index, got[index])
		}
	}
}

func TestMarkdownThematicBreakMarkers(t *testing.T) {
	for _, line := range []string{"--", "- -", "--- text", "-*-", "    ---"} {
		if isMarkdownThematicBreak(line) {
			t.Errorf("isMarkdownThematicBreak(%q) = true", line)
		}
	}
}

func TestWrapMarkdownBlockQuotes(t *testing.T) {
	got := wrapMarkdown("> alpha **beta** gamma\n>\n> > nested", 12)
	wantText := []string{"│ alpha beta", "│ gamma", "│", "│ │ nested"}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].text != want {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
		if len(got[index].spans) == 0 || got[index].spans[0].style.foreground != inlineForegroundMuted {
			t.Fatalf("line %d has no muted quote gutter: %+v", index, got[index])
		}
	}
	if !got[1].breakBefore.continuation || got[1].breakBefore.separator != " " {
		t.Fatalf("wrapped quote line = %+v", got[1])
	}

	betaBold := false
	for _, span := range got[0].spans {
		betaBold = betaBold || span.text == "beta" && span.style.bold
	}
	if !betaBold {
		t.Fatalf("quoted content lost inline style: %+v", got[0])
	}
}

func TestParseMarkdownBlockQuoteMarkers(t *testing.T) {
	tests := []struct {
		input       string
		wantDepth   int
		wantContent string
		wantOK      bool
	}{
		{input: "> quote", wantDepth: 1, wantContent: "quote", wantOK: true},
		{input: "  > > nested", wantDepth: 2, wantContent: "nested", wantOK: true},
		{input: ">>> compact", wantDepth: 3, wantContent: "compact", wantOK: true},
		{input: ">  preserved", wantDepth: 1, wantContent: " preserved", wantOK: true},
		{input: "    > indented code"},
	}
	for _, test := range tests {
		depth, content, ok := parseMarkdownBlockQuote(test.input)
		if depth != test.wantDepth || content != test.wantContent || ok != test.wantOK {
			t.Errorf("parseMarkdownBlockQuote(%q) = %d, %q, %t", test.input, depth, content, ok)
		}
	}
}

func TestWrapMarkdownTable(t *testing.T) {
	got := wrapMarkdown("| Name | State | Count |\n| :--- | :---: | ---: |\n| **alpha** | `ok` | 2 |", 80)
	wantText := []string{
		" Name  │ State │ Count ",
		"───────┼───────┼───────",
		" alpha │  ok   │     2 ",
	}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].text != want {
			t.Fatalf("line %d = %q, want %q", index, got[index].text, want)
		}
	}

	var headerBold, bodyBold, bodyCode bool
	for _, span := range got[0].spans {
		headerBold = headerBold || span.text == "Name" && span.style.bold
	}
	for _, span := range got[2].spans {
		bodyBold = bodyBold || span.text == "alpha" && span.style.bold
		bodyCode = bodyCode || span.text == "ok" && span.style.code
	}
	if !headerBold || !bodyBold || !bodyCode {
		t.Fatalf("table styles = %+v", got)
	}
}

func TestWrapMarkdownTableWrapsCells(t *testing.T) {
	got := wrapMarkdown("| First | Second |\n| --- | --- |\n| abcdef | ghijkl |", 11)
	wantText := []string{
		" Fir │ Sec ",
		" st  │ ond ",
		"─────┼─────",
		" abc │ ghi ",
		" def │ jkl ",
	}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].text != want || cellWidth(got[index].text) > 11 {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
	}
	if !got[1].breakBefore.continuation || !got[4].breakBefore.continuation {
		t.Fatalf("wrapped table lines lost continuation metadata: %+v", got)
	}
}

func TestWrapMarkdownTableStacksInNarrowWidth(t *testing.T) {
	got := wrapMarkdown("| Name | State | Count |\n| --- | --- | ---: |\n| alpha | `ok` | 2 |", 16)
	wantText := []string{"Name: alpha", "State: ok", "Count: 2"}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].text != want {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
	}
}

func TestSplitMarkdownTableRowHandlesEscapedPipes(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{input: "| key | \\| value |", want: []string{"key", "| value"}},
		{input: "| left \\\\| right |", want: []string{`left \`, "right"}},
		{input: "| `a|b` | x |", want: []string{"`a", "b`", "x"}},
	}
	for _, test := range tests {
		got, ok := splitMarkdownTableRow(test.input)
		if !ok || !reflect.DeepEqual(got, test.want) {
			t.Errorf("splitMarkdownTableRow(%q) = %q, %t; want %q", test.input, got, ok, test.want)
		}
	}
}

func TestWrapMarkdownRejectsInvalidTableDelimiter(t *testing.T) {
	got := wrapMarkdown("| A | B |\n| -- | --- |", 80)
	if len(got) != 2 || got[0].text != "| A | B |" || got[1].text != "| -- | --- |" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestWrapMarkdownRejectsIndentedTable(t *testing.T) {
	got := wrapMarkdown("    | A | B |\n    | --- | --- |", 80)
	if len(got) != 2 || got[0].text != "    | A | B |" || got[1].text != "    | --- | --- |" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestParseInlineMarkdown(t *testing.T) {
	got := parseInlineMarkdown("plain **bold** *italic* ***both***")
	want := []inlineSpan{
		{text: "plain ", style: inlineStyle{}},
		{text: "bold", style: inlineStyle{bold: true}},
		{text: " ", style: inlineStyle{}},
		{text: "italic", style: inlineStyle{italic: true}},
		{text: " ", style: inlineStyle{}},
		{text: "both", style: inlineStyle{bold: true, italic: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinks(t *testing.T) {
	got := parseInlineMarkdown("Read [the **docs**](https://example.com/docs) or visit https://example.com.")
	want := []inlineSpan{
		{text: "Read ", style: inlineStyle{}},
		{text: "the ", style: inlineStyle{link: "https://example.com/docs"}},
		{text: "docs", style: inlineStyle{bold: true, link: "https://example.com/docs"}},
		{text: " or visit ", style: inlineStyle{}},
		{text: "https://example.com", style: inlineStyle{link: "https://example.com"}},
		{text: ".", style: inlineStyle{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinkDestinations(t *testing.T) {
	tests := []struct {
		input string
		want  []inlineSpan
	}{
		{
			input: "[https://inner.example](https://outer.example)",
			want:  []inlineSpan{{text: "https://inner.example", style: inlineStyle{link: "https://outer.example"}}},
		},
		{
			input: "https://example.com/foo(bar)",
			want:  []inlineSpan{{text: "https://example.com/foo(bar)", style: inlineStyle{link: "https://example.com/foo(bar)"}}},
		},
		{
			input: "<mailto:user@example.com>",
			want:  []inlineSpan{{text: "mailto:user@example.com", style: inlineStyle{link: "mailto:user@example.com"}}},
		},
	}
	for _, test := range tests {
		if got := parseInlineMarkdown(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseInlineMarkdown(%q) = %+v, want %+v", test.input, got, test.want)
		}
	}
}

func TestParseInlineMarkdownCodeSpans(t *testing.T) {
	got := parseInlineMarkdown("Use `**name**` and ``a`b``")
	want := []inlineSpan{
		{text: "Use ", style: inlineStyle{}},
		{text: "**name**", style: inlineStyle{code: true}},
		{text: " and ", style: inlineStyle{}},
		{text: "a`b", style: inlineStyle{code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesCodeFences(t *testing.T) {
	got := parseInlineMarkdown("```bash\nabc\n```")
	want := []inlineSpan{{text: "```bash\nabc\n```", style: inlineStyle{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCode(t *testing.T) {
	got := wrapMarkdown("before **bold**\n```go\nfmt.Println(`value`)\n\n```\nafter", 80)
	want := []formattedLine{
		{text: "before bold", spans: []inlineSpan{
			{text: "before ", style: inlineStyle{}},
			{text: "bold", style: inlineStyle{bold: true}},
		}},
		{text: "fmt.Println(`value`)", fencedCode: true},
		{fencedCode: true},
		{text: "after", spans: []inlineSpan{{text: "after", style: inlineStyle{}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCodeUsesAvailableWidth(t *testing.T) {
	got := wrapMarkdown("```\nabcdefgh\n```", 6)
	want := []formattedLine{
		{text: "abcdef", fencedCode: true},
		{text: "gh", breakBefore: lineBreak{continuation: true}, fencedCode: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownNestedCodeInEmphasis(t *testing.T) {
	got := parseInlineMarkdown("**`backend/codex/api/models.go`** and *`SIGWINCH`*")
	want := []inlineSpan{
		{text: "backend/codex/api/models.go", style: inlineStyle{bold: true, code: true}},
		{text: " and ", style: inlineStyle{}},
		{text: "SIGWINCH", style: inlineStyle{italic: true, code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesUnmatchedMarkers(t *testing.T) {
	got := parseInlineMarkdown("keep **open and *unfinished and `code")
	if text := inlineSpanText(got); text != "keep **open and *unfinished and `code" {
		t.Fatalf("text = %q", text)
	}
	for _, span := range got {
		if span.style != (inlineStyle{}) {
			t.Fatalf("unexpected styled span: %+v", span)
		}
	}
}

func TestWrapInlineMarkdownCodeMarkersDoNotUseWidth(t *testing.T) {
	lines := wrapInlineMarkdown("`abcde`", 5)
	if len(lines) != 1 || lines[0].text != "abcde" || len(lines[0].spans) != 1 || !lines[0].spans[0].style.code {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWrapInlineMarkdownUsesRenderedWidth(t *testing.T) {
	lines := wrapInlineMarkdown("**abcd** *ef*", 5)
	if len(lines) != 2 || lines[0].text != "abcd" || lines[1].text != "ef" {
		t.Fatalf("lines = %+v", lines)
	}
	if len(lines[0].spans) != 1 || !lines[0].spans[0].style.bold {
		t.Fatalf("first line spans = %+v", lines[0].spans)
	}
	if len(lines[1].spans) != 1 || !lines[1].spans[0].style.italic || lines[1].breakBefore.separator != " " {
		t.Fatalf("second line = %+v", lines[1])
	}
}

func TestWrapInlineMarkdownUsesWordBoundaries(t *testing.T) {
	lines := wrapInlineMarkdown("alpha beta gamma", 8)
	want := []string{"alpha", "beta", "gamma"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v", lines)
	}
	for index, text := range want {
		if lines[index].text != text {
			t.Fatalf("line %d = %+v, want text %q", index, lines[index], text)
		}
		if index > 0 && (!lines[index].breakBefore.continuation || lines[index].breakBefore.separator != " ") {
			t.Fatalf("line %d does not preserve its soft-wrap separator: %+v", index, lines[index])
		}
	}
}

func TestWrapInlineMarkdownHardWrapsLongWords(t *testing.T) {
	lines := wrapInlineMarkdown("**abcdefgh**", 5)
	if len(lines) != 2 || lines[0].text != "abcde" || lines[1].text != "fgh" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].breakBefore.continuation || lines[1].breakBefore.separator != "" {
		t.Fatalf("long word continuation = %+v", lines[1])
	}
	for index, line := range lines {
		if len(line.spans) != 1 || !line.spans[0].style.bold {
			t.Fatalf("line %d lost its style: %+v", index, line)
		}
	}
}

func TestWrapInlineMarkdownUsesCellWidthAtWordBoundaries(t *testing.T) {
	lines := wrapInlineMarkdown("界界 test", 5)
	if len(lines) != 2 || lines[0].text != "界界" || lines[1].text != "test" || lines[1].breakBefore.separator != " " {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWrapInlineMarkdownPreservesWhitespaceBreaks(t *testing.T) {
	lines := wrapInlineMarkdown("a\tb\nc", 4)
	if len(lines) != 3 || lines[0].text != "a" || lines[1].text != "b" || lines[2].text != "c" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].breakBefore.continuation || lines[1].breakBefore.separator != "    " || lines[2].breakBefore != (lineBreak{}) {
		t.Fatalf("breaks = %+v", lines)
	}
}

func TestWrapInlineSpansKeepsZeroWidthRunesOnFullLines(t *testing.T) {
	bold := inlineStyle{bold: true}
	italic := inlineStyle{italic: true}
	lines := wrapInlineSpans([]inlineSpan{
		{text: "ab界", style: bold},
		{text: "\u0301cd", style: italic},
	}, 4)
	want := []formattedLine{
		{text: "ab界\u0301", spans: []inlineSpan{{text: "ab界", style: bold}, {text: "\u0301", style: italic}}},
		{text: "cd", spans: []inlineSpan{{text: "cd", style: italic}}, breakBefore: lineBreak{continuation: true}},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
}

func TestInlineSpanWrappingNormalizesInvalidUTF8(t *testing.T) {
	invalid := string([]byte{'a', 0xff, 'b'})
	lines := wrapInlineMarkdown(invalid, 2)
	if len(lines) != 2 || lines[0].text != "a�" || lines[1].text != "b" {
		t.Fatalf("lines = %+v", lines)
	}

	spans := truncateInlineSpans([]inlineSpan{{text: invalid}}, 2)
	if len(spans) != 1 || spans[0].text != "a�" {
		t.Fatalf("spans = %+v", spans)
	}
}
