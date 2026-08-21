package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
)

const (
	insertBeforeToolName = "insert_before"
	insertAfterToolName  = "insert_after"
)

var (
	insertBeforeToolDefinition = insertToolDefinition(
		insertBeforeToolName,
		"Insert lines before a uniquely anchored line, using that line's indentation; an empty anchor inserts at the beginning of the file.",
		"Text that must occur on exactly one line; empty targets the beginning of the file.",
	)
	insertAfterToolDefinition = insertToolDefinition(
		insertAfterToolName,
		"Insert lines after a uniquely anchored line, using the following line's indentation or the anchor's at EOF; an empty anchor inserts at the end of the file.",
		"Text that must occur on exactly one line; empty targets the end of the file.",
	)
)

type lineInsert struct {
	workspace workspace
	after     bool
}

type insertArguments struct {
	Path    string  `json:"path"`
	Anchor  *string `json:"anchor"`
	Content *string `json:"content"`
}

type anchoredLine struct {
	start      int
	end        int
	contentEnd int
}

func insertToolDefinition(name, description, anchorDescription string) agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters: StrictObject(map[string]agent.JSONSchema{
			"path":    {Type: "string", Description: "Relative or absolute file path."},
			"anchor":  {Type: "string", Description: anchorDescription},
			"content": {Type: "string", Description: "Lines to insert; line endings and base indentation are added automatically."},
		}, "path", "anchor", "content"),
	}
}

func NewInsertBefore(cwd string) *lineInsert {
	return &lineInsert{workspace: newWorkspace(cwd)}
}

func NewInsertAfter(cwd string) *lineInsert {
	return &lineInsert{workspace: newWorkspace(cwd), after: true}
}

func (i *lineInsert) Definition() agent.ToolDefinition {
	if i.after {
		return insertAfterToolDefinition
	}
	return insertBeforeToolDefinition
}

func (i *lineInsert) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return insertPresentation(i.Definition().Name, snapshotString(snapshot, "path"))
}

func insertPresentation(name, path string) agent.ToolPresentation {
	arguments := ""
	if path != "" {
		arguments = displayToolArgument(path)
	}

	return agent.ToolPresentation{Title: name, Arguments: arguments}
}

func (i *lineInsert) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	name := i.Definition().Name
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[insertArguments](arguments)
	if err != nil {
		return errorResult(name, err), nil
	}
	if args.Anchor == nil {
		return errorResult(name, fmt.Errorf("anchor is required and must be a string")), nil
	}
	if strings.ContainsAny(*args.Anchor, "\r\n") {
		return errorResult(name, fmt.Errorf("anchor must identify a single line")), nil
	}
	if args.Content == nil {
		return errorResult(name, fmt.Errorf("content is required and must be a string")), nil
	}

	requestedPath, err := i.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(name, err), nil
	}
	snapshot, err := textfile.Load(requestedPath)
	if err != nil {
		return errorResult(name, fmt.Errorf("%s: %w", i.workspace.display(requestedPath), err)), nil
	}
	original := snapshot.Data
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	line, err := findAnchoredLine(original, *args.Anchor)
	if err != nil {
		return errorResult(name, fmt.Errorf("%s: %w", i.workspace.display(requestedPath), err)), nil
	}
	if *args.Content == "" {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(i.workspace.display(requestedPath)))), nil
	}

	lineEnding := insertionLineEnding(original, line)
	indentation := []byte(nil)
	if line != nil {
		indentationLine := line
		if i.after {
			if next := nextLine(original, line); next != nil {
				indentationLine = next
			}
		}
		indentation = leadingIndentation(original[indentationLine.start:indentationLine.contentEnd])
	}
	insertion := formatInsertedLines(*args.Content, indentation, lineEnding)
	offset, needsLeadingLineEnding := insertionOffset(original, line, i.after)
	if needsLeadingLineEnding {
		insertion = append(append([]byte(nil), lineEnding...), insertion...)
	}

	replacement := make([]byte, 0, len(original)+len(insertion))
	replacement = append(replacement, original[:offset]...)
	replacement = append(replacement, insertion...)
	replacement = append(replacement, original[offset:]...)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := textfile.Replace(snapshot, replacement); err != nil {
		return errorResult(name, err), nil
	}
	if updates != nil {
		presentation := insertPresentation(name, args.Path)
		presentation.Diff = buildFileDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("inserted text in %s", escapeOutputName(i.workspace.display(requestedPath)))), nil
}

func findAnchoredLine(content []byte, anchor string) (*anchoredLine, error) {
	if anchor == "" {
		return nil, nil
	}

	var match *anchoredLine
	for start := 0; start < len(content); {
		newline := bytes.IndexByte(content[start:], '\n')
		end := len(content)
		contentEnd := end
		if newline >= 0 {
			contentEnd = start + newline
			end = contentEnd + 1
		}
		if contentEnd > start && content[contentEnd-1] == '\r' {
			contentEnd--
		}
		if bytes.Contains(content[start:contentEnd], []byte(anchor)) {
			if match != nil {
				return nil, fmt.Errorf("anchor identifies multiple lines; expected exactly one")
			}
			match = &anchoredLine{start: start, end: end, contentEnd: contentEnd}
		}
		start = end
	}
	if match == nil {
		return nil, fmt.Errorf("anchor was not found")
	}
	return match, nil
}

func nextLine(content []byte, line *anchoredLine) *anchoredLine {
	if line.end >= len(content) {
		return nil
	}

	start := line.end
	newline := bytes.IndexByte(content[start:], '\n')
	end := len(content)
	contentEnd := end
	if newline >= 0 {
		contentEnd = start + newline
		end = contentEnd + 1
	}
	if contentEnd > start && content[contentEnd-1] == '\r' {
		contentEnd--
	}
	return &anchoredLine{start: start, end: end, contentEnd: contentEnd}
}

func insertionOffset(content []byte, line *anchoredLine, after bool) (int, bool) {
	if line == nil {
		if after {
			return len(content), len(content) > 0 && content[len(content)-1] != '\n'
		}
		return 0, false
	}
	if !after {
		return line.start, false
	}
	return line.end, line.end > 0 && content[line.end-1] != '\n'
}

func insertionLineEnding(content []byte, line *anchoredLine) []byte {
	if line != nil && line.end > line.contentEnd {
		return content[line.contentEnd:line.end]
	}
	if newline := bytes.IndexByte(content, '\n'); newline >= 0 && newline > 0 && content[newline-1] == '\r' {
		return []byte("\r\n")
	}
	return []byte{'\n'}
}

func leadingIndentation(line []byte) []byte {
	end := 0
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	return line[:end]
}

func formatInsertedLines(content string, indentation, lineEnding []byte) []byte {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	commonIndentation := commonLeadingIndentation(lines)
	var formatted bytes.Buffer
	for _, line := range lines {
		if strings.Trim(line, " \t") != "" {
			formatted.Write(indentation)
			formatted.WriteString(strings.TrimPrefix(line, commonIndentation))
		}
		formatted.Write(lineEnding)
	}
	return formatted.Bytes()
}

func commonLeadingIndentation(lines []string) string {
	common := ""
	found := false
	for _, line := range lines {
		if strings.Trim(line, " \t") == "" {
			continue
		}

		indentation := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !found {
			common = indentation
			found = true
			continue
		}
		for !strings.HasPrefix(indentation, common) {
			common = common[:len(common)-1]
		}
	}
	return common
}
