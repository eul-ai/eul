package terminal

import (
	"cmp"
	"container/heap"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type fileSearchMatch struct {
	path          string
	display       string
	reference     string
	navigation    string
	name          string
	directory     bool
	hidden        bool
	score         int
	positions     []int
	depth         int
	displayLength int
	foldedDisplay string
}

func (m fileSearchMatch) identity() string {
	if m.path != "" {
		return m.path
	}
	return m.reference
}

func rankFileCandidates(ctx context.Context, cwd string, spec fileSearchSpec, candidates []fileCandidate) []fileSearchMatch {
	queryRunes := foldedRunes(filepath.ToSlash(spec.query))
	best := &fileSearchMatchHeap{spec: spec, matches: make([]fileSearchMatch, 0, min(len(candidates), filePickerMaxResults))}
	for index, candidate := range candidates {
		if index%256 == 0 {
			if ctx.Err() != nil {
				return nil
			}
		}
		if candidate.hidden && !spec.includeHidden {
			continue
		}

		match, ok := buildFileSearchMatch(cwd, spec, queryRunes, candidate)
		if !ok {
			continue
		}
		if best.Len() < filePickerMaxResults {
			heap.Push(best, match)
			continue
		}
		if compareFileSearchMatches(spec, match, best.matches[0]) >= 0 {
			continue
		}
		best.matches[0] = match
		heap.Fix(best, 0)
	}

	matches := best.matches
	slices.SortFunc(matches, func(left, right fileSearchMatch) int {
		return compareFileSearchMatches(spec, left, right)
	})
	return matches
}

func compareFileSearchMatches(spec fileSearchSpec, left, right fileSearchMatch) int {
	if order := cmp.Compare(right.score, left.score); order != 0 {
		return order
	}
	if left.directory != right.directory {
		if spec.query == "" {
			return compareTrueFirst(left.directory, right.directory)
		}
		return compareTrueFirst(!left.directory, !right.directory)
	}
	if left.hidden != right.hidden {
		return compareTrueFirst(!left.hidden, !right.hidden)
	}
	if order := cmp.Compare(left.depth, right.depth); order != 0 {
		return order
	}
	if order := cmp.Compare(left.displayLength, right.displayLength); order != 0 {
		return order
	}
	if order := strings.Compare(left.foldedDisplay, right.foldedDisplay); order != 0 {
		return order
	}
	return strings.Compare(left.display, right.display)
}

type fileSearchMatchHeap struct {
	spec    fileSearchSpec
	matches []fileSearchMatch
}

func (h fileSearchMatchHeap) Len() int {
	return len(h.matches)
}

func (h fileSearchMatchHeap) Less(left, right int) bool {
	return compareFileSearchMatches(h.spec, h.matches[left], h.matches[right]) > 0
}

func (h fileSearchMatchHeap) Swap(left, right int) {
	h.matches[left], h.matches[right] = h.matches[right], h.matches[left]
}

func (h *fileSearchMatchHeap) Push(value any) {
	h.matches = append(h.matches, value.(fileSearchMatch))
}

func (h *fileSearchMatchHeap) Pop() any {
	last := len(h.matches) - 1
	value := h.matches[last]
	h.matches = h.matches[:last]
	return value
}

func compareTrueFirst(left, right bool) int {
	switch {
	case left == right:
		return 0
	case left:
		return -1
	default:
		return 1
	}
}

func buildFileSearchMatch(cwd string, spec fileSearchSpec, queryRunes []rune, candidate fileCandidate) (fileSearchMatch, bool) {
	relativeRoot, err := filepath.Rel(spec.directory, candidate.path)
	if err != nil {
		return fileSearchMatch{}, false
	}
	relativeRoot = filepath.ToSlash(relativeRoot)
	if relativeRoot == "." || strings.HasPrefix(relativeRoot, "../") {
		return fileSearchMatch{}, false
	}

	scorePath := relativeRoot
	display := relativeRoot
	if !spec.explicit {
		relativeCWD, err := filepath.Rel(cwd, candidate.path)
		if err != nil || relativeCWD == "." || strings.HasPrefix(relativeCWD, ".."+string(filepath.Separator)) {
			return fileSearchMatch{}, false
		}
		display = filepath.ToSlash(relativeCWD)
		scorePath = display
	} else {
		display = spec.prefix + relativeRoot
	}

	score, positions, ok := scoreFileSearchPath(spec.query, queryRunes, scorePath, candidate.name)
	if !ok {
		return fileSearchMatch{}, false
	}

	displayOffset := utf8.RuneCountInString(strings.TrimSuffix(display, scorePath))
	for index := range positions {
		positions[index] += displayOffset
	}

	reference := filepath.ToSlash(candidate.path)
	if relativeCWD, err := filepath.Rel(cwd, candidate.path); err == nil && filepath.IsLocal(relativeCWD) {
		reference = filepath.ToSlash(relativeCWD)
	}
	navigation := display
	if candidate.directory && !spec.explicit {
		navigation = "./" + display
	}
	if candidate.directory {
		display += "/"
		reference += "/"
		navigation += "/"
	}

	return fileSearchMatch{
		path:          candidate.path,
		display:       display,
		reference:     reference,
		navigation:    navigation,
		name:          candidate.name,
		directory:     candidate.directory,
		hidden:        candidate.hidden,
		score:         score,
		positions:     positions,
		depth:         strings.Count(strings.TrimSuffix(display, "/"), "/"),
		displayLength: utf8.RuneCountInString(display),
		foldedDisplay: strings.ToLower(display),
	}, true
}

func scoreFileSearchPath(query string, queryRunes []rune, candidatePath, basename string) (int, []int, bool) {
	if query == "" {
		return 0, nil, true
	}

	pathRunes := foldedRunes(filepath.ToSlash(candidatePath))
	baseRunes := foldedRunes(basename)
	baseOffset := len(pathRunes) - len(baseRunes)
	exactCaseBonus := func(value string) int {
		if value == query {
			return 25
		}
		return 0
	}

	switch {
	case runesEqual(pathRunes, queryRunes):
		return 700_000 - len(pathRunes) + exactCaseBonus(candidatePath), runeRange(0, len(pathRunes)), true
	case runesEqual(baseRunes, queryRunes):
		return 650_000 - len(pathRunes) + exactCaseBonus(basename), runeRange(baseOffset, len(baseRunes)), true
	case runesHasPrefix(baseRunes, queryRunes):
		return 600_000 - len(pathRunes), runeRange(baseOffset, len(queryRunes)), true
	}

	if offset, ok := componentPrefix(pathRunes, queryRunes); ok {
		return 550_000 - len(pathRunes) - offset, runeRange(offset, len(queryRunes)), true
	}
	if offset := runesIndex(baseRunes, queryRunes); offset >= 0 {
		return 500_000 - len(pathRunes) - offset, runeRange(baseOffset+offset, len(queryRunes)), true
	}
	if offset := runesIndex(pathRunes, queryRunes); offset >= 0 {
		return 450_000 - len(pathRunes) - offset, runeRange(offset, len(queryRunes)), true
	}
	if positions, score, ok := fuzzySubsequence(queryRunes, baseRunes); ok {
		for index := range positions {
			positions[index] += baseOffset
		}
		return 350_000 + score - len(pathRunes), positions, true
	}
	if positions, score, ok := fuzzySubsequence(queryRunes, pathRunes); ok {
		return 300_000 + score - len(pathRunes), positions, true
	}
	return 0, nil, false
}

func foldedRunes(value string) []rune {
	result := []rune(value)
	for index, character := range result {
		result[index] = unicode.ToLower(character)
	}
	return result
}

func runesEqual(left, right []rune) bool {
	return slices.Equal(left, right)
}

func runesHasPrefix(value, prefix []rune) bool {
	return len(prefix) <= len(value) && slices.Equal(value[:len(prefix)], prefix)
}

func runesIndex(value, query []rune) int {
	if len(query) == 0 {
		return 0
	}
	for index := 0; index+len(query) <= len(value); index++ {
		if slices.Equal(value[index:index+len(query)], query) {
			return index
		}
	}
	return -1
}

func componentPrefix(value, query []rune) (int, bool) {
	if len(query) == 0 {
		return 0, true
	}
	for index := range value {
		if index > 0 && value[index-1] != '/' {
			continue
		}
		if runesHasPrefix(value[index:], query) {
			return index, true
		}
	}
	return 0, false
}

func fuzzySubsequence(query, value []rune) ([]int, int, bool) {
	if len(query) == 0 {
		return nil, 0, true
	}

	positions := make([]int, 0, len(query))
	searchFrom := 0
	score := 0
	for _, wanted := range query {
		found := -1
		for index := searchFrom; index < len(value); index++ {
			if value[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			return nil, 0, false
		}

		if len(positions) > 0 {
			gap := found - positions[len(positions)-1] - 1
			score -= gap * 4
			if gap == 0 {
				score += 20
			}
		}
		if found == 0 || value[found-1] == '/' || value[found-1] == '-' || value[found-1] == '_' || value[found-1] == '.' {
			score += 30
		}
		positions = append(positions, found)
		searchFrom = found + 1
	}
	score -= positions[0]
	return positions, score, true
}

func runeRange(start, length int) []int {
	result := make([]int, length)
	for index := range result {
		result[index] = start + index
	}
	return result
}
