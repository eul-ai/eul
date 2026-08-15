package terminal

import (
	"path"
	"strings"
)

func testFileSearchMatches(values ...string) []fileSearchMatch {
	matches := make([]fileSearchMatch, 0, len(values))
	for _, value := range values {
		directory := strings.HasSuffix(value, "/")
		trimmed := strings.TrimSuffix(value, "/")
		matches = append(matches, fileSearchMatch{
			display:    value,
			reference:  value,
			navigation: value,
			name:       path.Base(trimmed),
			directory:  directory,
		})
	}
	return matches
}

func testFileSearchResult(id uint64, values ...string) fileSearchResult {
	return fileSearchResult{id: id, matches: testFileSearchMatches(values...), state: fileSearchComplete}
}
