package terminal

import "testing"

func TestMarkdownSourceMapSelection(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		width       int
		startRow    int
		startColumn int
		endRow      int
		endColumn   int
		want        string
	}{
		{
			name:     "partial emphasis and list item",
			source:   "Here is **some** text.\n\n- first **item**\n- second item",
			width:    40,
			startRow: 0, startColumn: 8, endRow: 2, endColumn: 6,
			want: "**some** text.\n\n- first",
		},
		{
			name:     "heading",
			source:   "## Title\n\nbody",
			width:    40,
			startRow: 0, startColumn: 0, endRow: 0, endColumn: 4,
			want: "## Title",
		},
		{
			name:     "link label",
			source:   "see [docs](https://example.com) now",
			width:    40,
			startRow: 0, startColumn: 4, endRow: 0, endColumn: 7,
			want: "[docs](https://example.com)",
		},
		{
			name:     "wrapped emphasis",
			source:   "alpha **beta gamma** delta",
			width:    12,
			startRow: 0, startColumn: 6, endRow: 1, endColumn: 4,
			want: "**beta gamma**",
		},
		{
			name:     "block quote prefix",
			source:   "> quoted **text**",
			width:    40,
			startRow: 0, startColumn: 0, endRow: 0, endColumn: 12,
			want: "> quoted **text**",
		},
		{
			name:     "expanded tab",
			source:   "a\tb",
			width:    40,
			startRow: 0, startColumn: 5, endRow: 0, endColumn: 5,
			want: "b",
		},
		{
			name:     "inside code block",
			source:   "```go\nfmt.Println()\n```\nafter",
			width:    40,
			startRow: 0, startColumn: 0, endRow: 0, endColumn: 12,
			want: "fmt.Println()",
		},
		{
			name:     "from code block into prose",
			source:   "```go\nfmt.Println()\n```\nafter",
			width:    40,
			startRow: 0, startColumn: 0, endRow: 1, endColumn: 4,
			want: "```go\nfmt.Println()\n```\nafter",
		},
		{
			name:     "from prose into code block",
			source:   "before\n```go\nfmt.Println()\n```",
			width:    40,
			startRow: 0, startColumn: 0, endRow: 1, endColumn: 2,
			want: "before\n```go\nfmt\n```",
		},
		{
			name:     "table",
			source:   "| a | b |\n| --- | --- |\n| 1 | 2 |",
			width:    40,
			startRow: 2, startColumn: 1, endRow: 2, endColumn: 1,
			want: "| a | b |\n| --- | --- |\n| 1 | 2 |",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourceMap := newMarkdownSourceMap(test.source, test.width)
			got := sourceMap.selection(test.startRow, test.startColumn, test.endRow, test.endColumn)
			if got != test.want {
				t.Fatalf("selection = %q, want %q", got, test.want)
			}
		})
	}
}
