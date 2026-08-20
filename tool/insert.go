package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
)

const insertToolName = "insert"

var insertToolDefinition = agent.ToolDefinition{
	Name:        insertToolName,
	Description: "Insert text after a line.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"path":    {Type: "string", Description: "Relative or absolute file path."},
		"content": {Type: "string", Description: "Exact text to insert; no newlines are added."},
		"line":    nullable("integer", "Line to insert after; null appends, 0 prepends."),
	}, "path", "content", "line"),
}

type Insert struct {
	workspace workspace
}

type insertArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
	Line    *int    `json:"line"`
}

func NewInsert(cwd string) *Insert {
	return &Insert{workspace: newWorkspace(cwd)}
}

func (*Insert) Definition() agent.ToolDefinition {
	return insertToolDefinition
}

func (*Insert) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	var line *int
	if number, ok := snapshot.Arguments["line"].(json.Number); ok {
		if value, err := number.Int64(); err == nil && int64(int(value)) == value {
			parsed := int(value)
			line = &parsed
		}
	}

	return insertPresentation(snapshotString(snapshot, "path"), line)
}

func insertPresentation(path string, line *int) agent.ToolPresentation {
	arguments := ""
	if path != "" {
		arguments = displayToolArgument(path)
	}
	if line != nil {
		arguments += fmt.Sprintf(":%d", *line)
	}

	return agent.ToolPresentation{Title: insertToolName, Arguments: arguments}
}

func (i *Insert) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[insertArguments](arguments)
	if err != nil {
		return errorResult(insertToolName, err), nil
	}
	if args.Content == nil {
		return errorResult(insertToolName, fmt.Errorf("content is required and must be a string")), nil
	}

	requestedPath, err := i.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(insertToolName, err), nil
	}
	snapshot, err := textfile.Load(requestedPath)
	if err != nil {
		return errorResult(insertToolName, fmt.Errorf("%s: %w", i.workspace.display(requestedPath), err)), nil
	}
	original := snapshot.Data
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	offset := len(original)
	if args.Line != nil {
		offset, err = insertOffset(original, *args.Line)
		if err != nil {
			return errorResult(insertToolName, err), nil
		}
	}
	if *args.Content == "" {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(i.workspace.display(requestedPath)))), nil
	}

	replacement := make([]byte, 0, len(original)+len(*args.Content))
	replacement = append(replacement, original[:offset]...)
	replacement = append(replacement, *args.Content...)
	replacement = append(replacement, original[offset:]...)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := textfile.Replace(snapshot, replacement); err != nil {
		return errorResult(insertToolName, err), nil
	}
	if updates != nil {
		presentation := insertPresentation(args.Path, args.Line)
		presentation.Diff = buildFileDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("inserted text in %s", escapeOutputName(i.workspace.display(requestedPath)))), nil
}

func insertOffset(content []byte, line int) (int, error) {
	if line < 0 {
		return 0, fmt.Errorf("line must not be negative (requested: %d)", line)
	}

	lineCount := bytes.Count(content, []byte{'\n'})
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lineCount++
	}
	if line > lineCount {
		return 0, fmt.Errorf("line must not exceed %d (requested: %d)", lineCount, line)
	}
	if line == 0 {
		return 0, nil
	}
	if line == lineCount {
		return len(content), nil
	}

	offset := 0
	for range line {
		offset += bytes.IndexByte(content[offset:], '\n') + 1
	}
	return offset, nil
}
