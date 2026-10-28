package text

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
	got := WrapMarkdown("# Main *title*\n### Detail `code`\nparagraph", 80)
	want := []Line{
		{Spans: []Span{
			{Text: "Main ", Style: Style{Bold: true, Foreground: ForegroundAccent}},
			{Text: "title", Style: Style{Bold: true, Italic: true, Foreground: ForegroundAccent}},
		}},
		{Spans: []Span{
			{Text: "Detail ", Style: Style{Bold: true}},
			{Text: "code", Style: Style{Bold: true, Code: true}},
		}},
		{Spans: []Span{{Text: "paragraph", Style: Style{}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownHeadingMarkers(t *testing.T) {
	got := WrapMarkdown("   ## indented\n####### plain\n###plain", 80)
	if len(got) != 3 {
		t.Fatalf("lines = %+v", got)
	}
	if got[0].Text() != "indented" || len(got[0].Spans) != 1 || got[0].Spans[0].Style != (Style{Bold: true, Foreground: ForegroundAccent}) {
		t.Fatalf("heading = %+v", got[0])
	}
	if got[1].Text() != "####### plain" || got[2].Text() != "###plain" {
		t.Fatalf("non-headings = %+v", got[1:])
	}
}

func TestWrapMarkdownListItemsUseHangingIndent(t *testing.T) {
	got := WrapMarkdown("- **alpha beta**\n10. gamma delta\n  - child next", 9)
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
		if got[index].Text() != want {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
		if index%2 == 1 && (!got[index].Break.Continuation || got[index].Break.Separator != " ") {
			t.Fatalf("line %d lost continuation metadata: %+v", index, got[index])
		}
	}
	betaBold := false
	for _, span := range got[1].Spans {
		betaBold = betaBold || span.Text == "beta" && span.Style.Bold
	}
	if !betaBold {
		t.Fatalf("wrapped item lost inline Style: %+v", got[1])
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
	got := WrapMarkdown("before\n---\n * * * \n  ___\nafter", 8)
	if len(got) != 5 || got[0].Text() != "before" || got[4].Text() != "after" {
		t.Fatalf("lines = %+v", got)
	}
	for index := 1; index <= 3; index++ {
		if got[index].Text() != "────────" || !got[index].ThematicBreak {
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
	got := WrapMarkdown("> alpha **beta** gamma\n>\n> > nested", 12)
	wantText := []string{"│ alpha beta", "│ gamma", "│", "│ │ nested"}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].Text() != want {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
		if len(got[index].Spans) == 0 || got[index].Spans[0].Style.Foreground != ForegroundMuted {
			t.Fatalf("line %d has no muted quote gutter: %+v", index, got[index])
		}
	}
	if !got[1].Break.Continuation || got[1].Break.Separator != " " {
		t.Fatalf("wrapped quote line = %+v", got[1])
	}

	betaBold := false
	for _, span := range got[0].Spans {
		betaBold = betaBold || span.Text == "beta" && span.Style.Bold
	}
	if !betaBold {
		t.Fatalf("quoted content lost inline Style: %+v", got[0])
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
	got := WrapMarkdown("| Name | State | Count |\n| :--- | :---: | ---: |\n| **alpha** | `ok` | 2 |", 80)
	wantText := []string{
		" Name  │ State │ Count ",
		"───────┼───────┼───────",
		" alpha │  ok   │     2 ",
	}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].Text() != want {
			t.Fatalf("line %d = %q, want %q", index, got[index].Text(), want)
		}
	}

	var headerBold, bodyBold, bodyCode bool
	for _, span := range got[0].Spans {
		headerBold = headerBold || span.Text == "Name" && span.Style.Bold
	}
	for _, span := range got[2].Spans {
		bodyBold = bodyBold || span.Text == "alpha" && span.Style.Bold
		bodyCode = bodyCode || span.Text == "ok" && span.Style.Code
	}
	if !headerBold || !bodyBold || !bodyCode {
		t.Fatalf("table styles = %+v", got)
	}
}

func TestWrapMarkdownTableWrapsCells(t *testing.T) {
	got := WrapMarkdown("| First | Second |\n| --- | --- |\n| abcdef | ghijkl |", 11)
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
		if got[index].Text() != want || Width(got[index].Text()) > 11 {
			t.Fatalf("line %d = %+v, want text %q", index, got[index], want)
		}
	}
	if !got[1].Break.Continuation || !got[4].Break.Continuation {
		t.Fatalf("wrapped table lines lost continuation metadata: %+v", got)
	}
}

func TestWrapMarkdownTableStacksInNarrowWidth(t *testing.T) {
	got := WrapMarkdown("| Name | State | Count |\n| --- | --- | ---: |\n| alpha | `ok` | 2 |", 16)
	wantText := []string{"Name: alpha", "State: ok", "Count: 2"}
	if len(got) != len(wantText) {
		t.Fatalf("lines = %+v", got)
	}
	for index, want := range wantText {
		if got[index].Text() != want {
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
	got := WrapMarkdown("| A | B |\n| -- | --- |", 80)
	if len(got) != 2 || got[0].Text() != "| A | B |" || got[1].Text() != "| -- | --- |" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestWrapMarkdownRejectsIndentedTable(t *testing.T) {
	got := WrapMarkdown("    | A | B |\n    | --- | --- |", 80)
	if len(got) != 2 || got[0].Text() != "    | A | B |" || got[1].Text() != "    | --- | --- |" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestParseInlineMarkdown(t *testing.T) {
	got := ParseInline("plain **bold** *italic* ***both***")
	want := []Span{
		{Text: "plain ", Style: Style{}},
		{Text: "bold", Style: Style{Bold: true}},
		{Text: " ", Style: Style{}},
		{Text: "italic", Style: Style{Italic: true}},
		{Text: " ", Style: Style{}},
		{Text: "both", Style: Style{Bold: true, Italic: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinks(t *testing.T) {
	got := ParseInline("Read [the **docs**](https://example.com/docs) or visit https://example.com.")
	want := []Span{
		{Text: "Read ", Style: Style{}},
		{Text: "the ", Style: Style{Link: "https://example.com/docs"}},
		{Text: "docs", Style: Style{Bold: true, Link: "https://example.com/docs"}},
		{Text: " or visit ", Style: Style{}},
		{Text: "https://example.com", Style: Style{Link: "https://example.com"}},
		{Text: ".", Style: Style{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownLinkDestinations(t *testing.T) {
	tests := []struct {
		input string
		want  []Span
	}{
		{
			input: "[https://inner.example](https://outer.example)",
			want:  []Span{{Text: "https://inner.example", Style: Style{Link: "https://outer.example"}}},
		},
		{
			input: "https://example.com/foo(bar)",
			want:  []Span{{Text: "https://example.com/foo(bar)", Style: Style{Link: "https://example.com/foo(bar)"}}},
		},
		{
			input: "<mailto:user@example.com>",
			want:  []Span{{Text: "mailto:user@example.com", Style: Style{Link: "mailto:user@example.com"}}},
		},
	}
	for _, test := range tests {
		if got := ParseInline(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseInline(%q) = %+v, want %+v", test.input, got, test.want)
		}
	}
}

func TestParseInlineMarkdownCodeSpans(t *testing.T) {
	got := ParseInline("Use `**name**` and ``a`b``")
	want := []Span{
		{Text: "Use ", Style: Style{}},
		{Text: "**name**", Style: Style{Code: true}},
		{Text: " and ", Style: Style{}},
		{Text: "a`b", Style: Style{Code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesCodeFences(t *testing.T) {
	got := ParseInline("```bash\nabc\n```")
	want := []Span{{Text: "```bash\nabc\n```", Style: Style{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCode(t *testing.T) {
	got := WrapMarkdown("before **bold**\n```go\nfmt.Println(`value`)\n\n```\nafter", 80)
	want := []Line{
		{Spans: []Span{
			{Text: "before ", Style: Style{}},
			{Text: "bold", Style: Style{Bold: true}},
		}},
		{Spans: []Span{{Text: "fmt.Println(`value`)"}}, FencedCode: true},
		{FencedCode: true},
		{Spans: []Span{{Text: "after", Style: Style{}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestWrapMarkdownFencedCodeUsesAvailableWidth(t *testing.T) {
	got := WrapMarkdown("```\nabcdefgh\n```", 6)
	want := []Line{
		{Spans: []Span{{Text: "abcdef"}}, FencedCode: true},
		{Spans: []Span{{Text: "gh"}}, Break: Break{Continuation: true}, FencedCode: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownNestedCodeInEmphasis(t *testing.T) {
	got := ParseInline("**`backend/codex/api/models.go`** and *`SIGWINCH`*")
	want := []Span{
		{Text: "backend/codex/api/models.go", Style: Style{Bold: true, Code: true}},
		{Text: " and ", Style: Style{}},
		{Text: "SIGWINCH", Style: Style{Italic: true, Code: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v, want %+v", got, want)
	}
}

func TestParseInlineMarkdownPreservesUnmatchedMarkers(t *testing.T) {
	got := ParseInline("keep **open and *unfinished and `code")
	if text := Plain(got); text != "keep **open and *unfinished and `code" {
		t.Fatalf("text = %q", text)
	}
	for _, span := range got {
		if span.Style != (Style{}) {
			t.Fatalf("unexpected styled span: %+v", span)
		}
	}
}

func TestWrapInlineMarkdownCodeMarkersDoNotUseWidth(t *testing.T) {
	lines := wrapInlineMarkdown("`abcde`", 5)
	if len(lines) != 1 || lines[0].Text() != "abcde" || len(lines[0].Spans) != 1 || !lines[0].Spans[0].Style.Code {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWrapInlineMarkdownUsesRenderedWidth(t *testing.T) {
	lines := wrapInlineMarkdown("**abcd** *ef*", 5)
	if len(lines) != 2 || lines[0].Text() != "abcd" || lines[1].Text() != "ef" {
		t.Fatalf("lines = %+v", lines)
	}
	if len(lines[0].Spans) != 1 || !lines[0].Spans[0].Style.Bold {
		t.Fatalf("first line spans = %+v", lines[0].Spans)
	}
	if len(lines[1].Spans) != 1 || !lines[1].Spans[0].Style.Italic || lines[1].Break.Separator != " " {
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
		if lines[index].Text() != text {
			t.Fatalf("line %d = %+v, want text %q", index, lines[index], text)
		}
		if index > 0 && (!lines[index].Break.Continuation || lines[index].Break.Separator != " ") {
			t.Fatalf("line %d does not preserve its soft-wrap separator: %+v", index, lines[index])
		}
	}
}

func TestWrapInlineMarkdownHardWrapsLongWords(t *testing.T) {
	lines := wrapInlineMarkdown("**abcdefgh**", 5)
	if len(lines) != 2 || lines[0].Text() != "abcde" || lines[1].Text() != "fgh" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].Break.Continuation || lines[1].Break.Separator != "" {
		t.Fatalf("long word continuation = %+v", lines[1])
	}
	for index, line := range lines {
		if len(line.Spans) != 1 || !line.Spans[0].Style.Bold {
			t.Fatalf("line %d lost its Style: %+v", index, line)
		}
	}
}

func TestWrapInlineMarkdownUsesCellWidthAtWordBoundaries(t *testing.T) {
	lines := wrapInlineMarkdown("界界 test", 5)
	if len(lines) != 2 || lines[0].Text() != "界界" || lines[1].Text() != "test" || lines[1].Break.Separator != " " {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestWrapInlineMarkdownPreservesWhitespaceBreaks(t *testing.T) {
	lines := wrapInlineMarkdown("a\tb\nc", 4)
	if len(lines) != 3 || lines[0].Text() != "a" || lines[1].Text() != "b" || lines[2].Text() != "c" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[1].Break.Continuation || lines[1].Break.Separator != "    " || lines[2].Break != (Break{}) {
		t.Fatalf("breaks = %+v", lines)
	}
}

func TestWrapInlineSpansKeepsZeroWidthRunesOnFullLines(t *testing.T) {
	bold := Style{Bold: true}
	italic := Style{Italic: true}
	lines := WrapSpans([]Span{
		{Text: "ab界", Style: bold},
		{Text: "\u0301cd", Style: italic},
	}, 4)
	want := []Line{
		{Spans: []Span{{Text: "ab界", Style: bold}, {Text: "\u0301", Style: italic}}},
		{Spans: []Span{{Text: "cd", Style: italic}}, Break: Break{Continuation: true}},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %+v, want %+v", lines, want)
	}
}

func TestInlineSpanWrappingNormalizesInvalidUTF8(t *testing.T) {
	invalid := string([]byte{'a', 0xff, 'b'})
	lines := wrapInlineMarkdown(invalid, 2)
	if len(lines) != 2 || lines[0].Text() != "a�" || lines[1].Text() != "b" {
		t.Fatalf("lines = %+v", lines)
	}

	spans := TruncateSpans([]Span{{Text: invalid}}, 2)
	if len(spans) != 1 || spans[0].Text != "a�" {
		t.Fatalf("spans = %+v", spans)
	}
}
