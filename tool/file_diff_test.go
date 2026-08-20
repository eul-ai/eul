package tool

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestFileDiffOperationsProduceMinimalValidScripts(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	vocabulary := []fileDiffSourceLine{
		{raw: "a\n", text: "a"},
		{raw: "b\n", text: "b"},
		{raw: "c\n", text: "c"},
	}

	for oldLength := range 7 {
		for newLength := range 7 {
			for range 100 {
				oldLines := randomFileDiffLines(random, vocabulary, oldLength)
				newLines := randomFileDiffLines(random, vocabulary, newLength)
				work := fileDiffMaxWork
				var operations []fileDiffOperation
				appendFileDiffOperations(&operations, oldLines, newLines, &work)

				var reconstructedOld, reconstructedNew []fileDiffSourceLine
				changes := 0
				for _, operation := range operations {
					switch operation.kind {
					case agent.ToolDiffLineContext:
						reconstructedOld = append(reconstructedOld, operation.lines...)
						reconstructedNew = append(reconstructedNew, operation.lines...)
					case agent.ToolDiffLineRemoved:
						reconstructedOld = append(reconstructedOld, operation.lines...)
						changes += len(operation.lines)
					case agent.ToolDiffLineAdded:
						reconstructedNew = append(reconstructedNew, operation.lines...)
						changes += len(operation.lines)
					}
				}

				if !slices.Equal(reconstructedOld, oldLines) || !slices.Equal(reconstructedNew, newLines) {
					t.Fatalf("invalid script: old=%v new=%v operations=%+v", oldLines, newLines, operations)
				}
				wantChanges := len(oldLines) + len(newLines) - 2*fileDiffLCSLength(oldLines, newLines)
				if changes != wantChanges {
					t.Fatalf("non-minimal script: old=%v new=%v changes=%d want=%d operations=%+v", oldLines, newLines, changes, wantChanges, operations)
				}
			}
		}
	}
}

func randomFileDiffLines(random *rand.Rand, vocabulary []fileDiffSourceLine, length int) []fileDiffSourceLine {
	lines := make([]fileDiffSourceLine, length)
	for index := range lines {
		lines[index] = vocabulary[random.Intn(len(vocabulary))]
	}
	return lines
}

func fileDiffLCSLength(left, right []fileDiffSourceLine) int {
	lengths := make([][]int, len(left)+1)
	for index := range lengths {
		lengths[index] = make([]int, len(right)+1)
	}
	for leftIndex, leftLine := range left {
		for rightIndex, rightLine := range right {
			if leftLine.raw == rightLine.raw {
				lengths[leftIndex+1][rightIndex+1] = lengths[leftIndex][rightIndex] + 1
				continue
			}
			lengths[leftIndex+1][rightIndex+1] = max(lengths[leftIndex][rightIndex+1], lengths[leftIndex+1][rightIndex])
		}
	}
	return lengths[len(left)][len(right)]
}
