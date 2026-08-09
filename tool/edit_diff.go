package tool

import (
	"bytes"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const (
	editDiffContextLines     = 4
	editDiffMaxWork          = defaultMaxLines * defaultMaxLines
	editDiffTruncationMarker = "… (diff truncated)"
	editDiffHunkMarker       = "..."
)

type editDiffSourceLine struct {
	raw  string
	text string
}

type editDiffOperation struct {
	kind  agent.ToolDiffLineKind
	lines []editDiffSourceLine
}

type editDiffBuilder struct {
	lines []agent.ToolDiffLine
	bytes int
}

func buildEditDiff(original, replacement []byte) []agent.ToolDiffLine {
	if bytes.Equal(original, replacement) {
		return nil
	}

	oldLines := splitEditDiffLines(original)
	newLines := splitEditDiffLines(replacement)
	prefix := commonEditDiffPrefix(oldLines, newLines)
	suffix := commonEditDiffSuffix(oldLines[prefix:], newLines[prefix:])
	oldEnd := len(oldLines) - suffix
	newEnd := len(newLines) - suffix

	work := editDiffMaxWork
	var operations []editDiffOperation
	appendEditDiffOperations(&operations, oldLines[prefix:oldEnd], newLines[prefix:newEnd], &work)

	builder := editDiffBuilder{lines: make([]agent.ToolDiffLine, 0, min(defaultMaxLines, len(operations)*2+editDiffContextLines*2))}
	contextStart := max(0, prefix-editDiffContextLines)
	for index := contextStart; index < prefix; index++ {
		if !builder.append(agent.ToolDiffLine{
			Kind:    agent.ToolDiffLineContext,
			OldLine: index + 1,
			NewLine: index + 1,
			Text:    oldLines[index].text,
		}) {
			return builder.lines
		}
	}

	oldLine := prefix + 1
	newLine := prefix + 1
	for index := 0; index < len(operations); {
		if operations[index].kind == agent.ToolDiffLineContext {
			if !appendEditDiffContext(&builder, operations[index].lines, &oldLine, &newLine) {
				return builder.lines
			}
			index++
			continue
		}

		end := index
		for end < len(operations) && operations[end].kind != agent.ToolDiffLineContext {
			end++
		}
		for _, operation := range operations[index:end] {
			if operation.kind != agent.ToolDiffLineRemoved {
				continue
			}
			for _, line := range operation.lines {
				if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineRemoved, OldLine: oldLine, Text: line.text}) {
					return builder.lines
				}
				oldLine++
			}
		}
		for _, operation := range operations[index:end] {
			if operation.kind != agent.ToolDiffLineAdded {
				continue
			}
			for _, line := range operation.lines {
				if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineAdded, NewLine: newLine, Text: line.text}) {
					return builder.lines
				}
				newLine++
			}
		}
		index = end
	}

	for offset := range min(suffix, editDiffContextLines) {
		oldIndex := oldEnd + offset
		newIndex := newEnd + offset
		if !builder.append(agent.ToolDiffLine{
			Kind:    agent.ToolDiffLineContext,
			OldLine: oldIndex + 1,
			NewLine: newIndex + 1,
			Text:    oldLines[oldIndex].text,
		}) {
			return builder.lines
		}
	}
	return builder.lines
}

func appendEditDiffContext(builder *editDiffBuilder, lines []editDiffSourceLine, oldLine, newLine *int) bool {
	if len(lines) <= editDiffContextLines*2 {
		for _, line := range lines {
			if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineContext, OldLine: *oldLine, NewLine: *newLine, Text: line.text}) {
				return false
			}
			(*oldLine)++
			(*newLine)++
		}
		return true
	}

	for _, line := range lines[:editDiffContextLines] {
		if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineContext, OldLine: *oldLine, NewLine: *newLine, Text: line.text}) {
			return false
		}
		(*oldLine)++
		(*newLine)++
	}
	skipped := len(lines) - editDiffContextLines*2
	*oldLine += skipped
	*newLine += skipped
	if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineOmitted, Text: editDiffHunkMarker}) {
		return false
	}
	for _, line := range lines[len(lines)-editDiffContextLines:] {
		if !builder.append(agent.ToolDiffLine{Kind: agent.ToolDiffLineContext, OldLine: *oldLine, NewLine: *newLine, Text: line.text}) {
			return false
		}
		(*oldLine)++
		(*newLine)++
	}
	return true
}

func appendEditDiffOperations(operations *[]editDiffOperation, oldLines, newLines []editDiffSourceLine, work *int) {
	prefix := commonEditDiffPrefix(oldLines, newLines)
	appendEditDiffOperation(operations, agent.ToolDiffLineContext, oldLines[:prefix])
	oldLines = oldLines[prefix:]
	newLines = newLines[prefix:]

	suffix := commonEditDiffSuffix(oldLines, newLines)
	oldMiddle := oldLines[:len(oldLines)-suffix]
	newMiddle := newLines[:len(newLines)-suffix]
	oldSuffix := oldLines[len(oldLines)-suffix:]

	switch {
	case len(oldMiddle) == 0:
		appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle)
	case len(newMiddle) == 0:
		appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle)
	case len(oldMiddle) == 1:
		index := indexEditDiffLine(newMiddle, oldMiddle[0])
		if index < 0 {
			appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle)
			appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle)
		} else {
			appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle[:index])
			appendEditDiffOperation(operations, agent.ToolDiffLineContext, oldMiddle)
			appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle[index+1:])
		}
	case len(newMiddle) == 1:
		index := indexEditDiffLine(oldMiddle, newMiddle[0])
		if index < 0 {
			appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle)
			appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle)
		} else {
			appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle[:index])
			appendEditDiffOperation(operations, agent.ToolDiffLineContext, newMiddle)
			appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle[index+1:])
		}
	default:
		x, y, ok := bisectEditDiff(oldMiddle, newMiddle, work)
		if !ok || x == 0 && y == 0 || x == len(oldMiddle) && y == len(newMiddle) {
			appendEditDiffOperation(operations, agent.ToolDiffLineRemoved, oldMiddle)
			appendEditDiffOperation(operations, agent.ToolDiffLineAdded, newMiddle)
		} else {
			appendEditDiffOperations(operations, oldMiddle[:x], newMiddle[:y], work)
			appendEditDiffOperations(operations, oldMiddle[x:], newMiddle[y:], work)
		}
	}
	appendEditDiffOperation(operations, agent.ToolDiffLineContext, oldSuffix)
}

func appendEditDiffOperation(operations *[]editDiffOperation, kind agent.ToolDiffLineKind, lines []editDiffSourceLine) {
	if len(lines) == 0 {
		return
	}
	if len(*operations) > 0 && (*operations)[len(*operations)-1].kind == kind {
		last := &(*operations)[len(*operations)-1]
		merged := make([]editDiffSourceLine, 0, len(last.lines)+len(lines))
		merged = append(merged, last.lines...)
		last.lines = append(merged, lines...)
		return
	}
	*operations = append(*operations, editDiffOperation{kind: kind, lines: lines})
}

func bisectEditDiff(oldLines, newLines []editDiffSourceLine, work *int) (int, int, bool) {
	oldLength := len(oldLines)
	newLength := len(newLines)
	maxDistance := (oldLength + newLength + 1) / 2
	offset := maxDistance
	vectorLength := maxDistance*2 + 1
	forward := make([]int, vectorLength)
	reverse := make([]int, vectorLength)
	for index := range forward {
		forward[index] = -1
		reverse[index] = -1
	}
	forward[offset+1] = 0
	reverse[offset+1] = 0

	delta := oldLength - newLength
	frontOverlap := delta%2 != 0
	forwardStart, forwardEnd := 0, 0
	reverseStart, reverseEnd := 0, 0
	for distance := range maxDistance {
		for diagonal := -distance + forwardStart; diagonal <= distance-forwardEnd; diagonal += 2 {
			if !takeEditDiffWork(work) {
				return 0, 0, false
			}
			vectorIndex := offset + diagonal
			x := 0
			if diagonal == -distance || diagonal != distance && forward[vectorIndex-1] < forward[vectorIndex+1] {
				x = forward[vectorIndex+1]
			} else {
				x = forward[vectorIndex-1] + 1
			}
			y := x - diagonal
			for x < oldLength && y < newLength && oldLines[x].raw == newLines[y].raw {
				if !takeEditDiffWork(work) {
					return 0, 0, false
				}
				x++
				y++
			}
			forward[vectorIndex] = x
			switch {
			case x > oldLength:
				forwardEnd += 2
			case y > newLength:
				forwardStart += 2
			case frontOverlap:
				reverseIndex := offset + delta - diagonal
				if reverseIndex >= 0 && reverseIndex < vectorLength && reverse[reverseIndex] != -1 && x >= oldLength-reverse[reverseIndex] {
					return x, y, true
				}
			}
		}

		for diagonal := -distance + reverseStart; diagonal <= distance-reverseEnd; diagonal += 2 {
			if !takeEditDiffWork(work) {
				return 0, 0, false
			}
			vectorIndex := offset + diagonal
			x := 0
			if diagonal == -distance || diagonal != distance && reverse[vectorIndex-1] < reverse[vectorIndex+1] {
				x = reverse[vectorIndex+1]
			} else {
				x = reverse[vectorIndex-1] + 1
			}
			y := x - diagonal
			for x < oldLength && y < newLength && oldLines[oldLength-x-1].raw == newLines[newLength-y-1].raw {
				if !takeEditDiffWork(work) {
					return 0, 0, false
				}
				x++
				y++
			}
			reverse[vectorIndex] = x
			switch {
			case x > oldLength:
				reverseEnd += 2
			case y > newLength:
				reverseStart += 2
			case !frontOverlap:
				forwardIndex := offset + delta - diagonal
				if forwardIndex >= 0 && forwardIndex < vectorLength && forward[forwardIndex] != -1 {
					forwardX := forward[forwardIndex]
					forwardY := forwardX - (forwardIndex - offset)
					if forwardX >= oldLength-x {
						return forwardX, forwardY, true
					}
				}
			}
		}
	}
	return 0, 0, false
}

func takeEditDiffWork(work *int) bool {
	if *work <= 0 {
		return false
	}
	(*work)--
	return true
}

func commonEditDiffPrefix(left, right []editDiffSourceLine) int {
	length := min(len(left), len(right))
	for index := range length {
		if left[index].raw != right[index].raw {
			return index
		}
	}
	return length
}

func commonEditDiffSuffix(left, right []editDiffSourceLine) int {
	length := min(len(left), len(right))
	for offset := range length {
		if left[len(left)-offset-1].raw != right[len(right)-offset-1].raw {
			return offset
		}
	}
	return length
}

func indexEditDiffLine(lines []editDiffSourceLine, target editDiffSourceLine) int {
	for index, line := range lines {
		if line.raw == target.raw {
			return index
		}
	}
	return -1
}

func (builder *editDiffBuilder) append(line agent.ToolDiffLine) bool {
	bodyBytes := defaultMaxBytes - len(editDiffTruncationMarker)
	if len(builder.lines) < defaultMaxLines-1 && builder.bytes+len(line.Text) <= bodyBytes {
		builder.lines = append(builder.lines, line)
		builder.bytes += len(line.Text)
		return true
	}

	remainingBytes := bodyBytes - builder.bytes
	if len(builder.lines) < defaultMaxLines-1 && remainingBytes > 0 {
		line.Text, _ = truncateLine(line.Text, remainingBytes)
		builder.lines = append(builder.lines, line)
	}
	builder.lines = append(builder.lines, agent.ToolDiffLine{Kind: agent.ToolDiffLineOmitted, Text: editDiffTruncationMarker})
	return false
}

func splitEditDiffLines(content []byte) []editDiffSourceLine {
	source := string(content)
	lines := make([]editDiffSourceLine, 0, strings.Count(source, "\n")+1)
	for start := 0; start < len(source); {
		newline := strings.IndexByte(source[start:], '\n')
		if newline < 0 {
			lines = append(lines, editDiffSourceLine{raw: source[start:], text: source[start:]})
			break
		}

		newline += start
		end := newline + 1
		textEnd := newline
		if textEnd > start && source[textEnd-1] == '\r' {
			textEnd--
		}
		lines = append(lines, editDiffSourceLine{raw: source[start:end], text: source[start:textEnd]})
		start = end
	}
	return lines
}
