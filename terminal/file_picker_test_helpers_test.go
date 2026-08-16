package terminal

import (
	"path"
	"strings"

	"github.com/eul-ai/eul/filesearch"
)

func testFileSearchMatches(values ...string) []filesearch.Match {
	matches := make([]filesearch.Match, 0, len(values))
	for _, value := range values {
		directory := strings.HasSuffix(value, "/")
		trimmed := strings.TrimSuffix(value, "/")
		matches = append(matches, filesearch.Match{
			Display:     value,
			Reference:   value,
			BrowseQuery: value,
			Name:        path.Base(trimmed),
			IsDir:       directory,
		})
	}
	return matches
}

func testFileSearchResult(id uint64, values ...string) filesearch.Result {
	return filesearch.Result{ID: id, Matches: testFileSearchMatches(values...), State: filesearch.StateComplete}
}
