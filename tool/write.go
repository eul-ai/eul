package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yaah/agent"
)

const (
	writeToolName             = "write"
	writePresentationMaxLines = 10
	writePresentationMaxBytes = 4 * 1024
)

var writeToolDefinition = agent.ToolDefinition{
	Name:        writeToolName,
	Description: "Create or overwrite a regular file and parent directories. Writes are not transactional.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":    {Type: "string", Description: "File path, relative to the session working directory or absolute."},
		"content": {Type: "string", Description: "Complete file content; an empty string writes an empty file."},
	}, "path", "content"),
}

type Write struct {
	workspace workspace
}

type writeArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func NewWrite(cwd string) *Write {
	return &Write{workspace: newWorkspace(cwd)}
}

func (*Write) Definition() agent.ToolDefinition {
	return writeToolDefinition
}

func (*Write) Presentation(snapshot agent.ToolCallSnapshot) agent.ToolPresentation {
	arguments := ""
	if path := snapshotString(snapshot, "path"); path != "" {
		arguments = displayToolArgument(path)
	}

	content, exists := snapshot.Arguments["content"]
	if !exists {
		return agent.ToolPresentation{Title: writeToolName, Arguments: arguments}
	}
	text, ok := content.(string)
	if !ok {
		if snapshot.Complete {
			return agent.ToolPresentation{Title: writeToolName, Arguments: arguments, Lines: []string{"[invalid content argument]"}}
		}
		return agent.ToolPresentation{Title: writeToolName, Arguments: arguments}
	}

	return agent.ToolPresentation{Title: writeToolName, Arguments: arguments, Lines: writePreview(text)}
}

func writePreview(content string) []string {
	if content == "" {
		return nil
	}

	allLines := strings.Split(content, "\n")
	totalLines := len(allLines)
	if totalLines <= writePresentationMaxLines && len(content) <= writePresentationMaxBytes {
		return allLines
	}

	lineMarker := writePreviewLineMarker(totalLines, totalLines)
	byteMarker := writePreviewByteMarker(totalLines)
	markerBytes := max(len(lineMarker), len(byteMarker))
	bodyBytes := writePresentationMaxBytes - markerBytes - 1
	lineLimit := min(totalLines, writePresentationMaxLines)

	lines := make([]string, 0, lineLimit+1)
	usedBytes := 0
	completeLines := 0
	partialLine := false
	for _, line := range allLines[:lineLimit] {
		separatorBytes := 0
		if len(lines) > 0 {
			separatorBytes = 1
		}
		available := bodyBytes - usedBytes - separatorBytes
		if available <= 0 {
			break
		}
		bounded, truncated := truncateLine(line, available)
		lines = append(lines, bounded)
		usedBytes += separatorBytes + len(bounded)
		if truncated {
			partialLine = true
			break
		}
		completeLines++
	}

	marker := writePreviewLineMarker(totalLines-completeLines, totalLines)
	if partialLine {
		marker = byteMarker
	}
	lines = append(lines, marker)
	return lines
}

func writePreviewLineMarker(remaining, total int) string {
	unit := "lines"
	if remaining == 1 {
		unit = "line"
	}
	return fmt.Sprintf("… (%d more %s, %d total)", remaining, unit, total)
}

func writePreviewByteMarker(total int) string {
	unit := "lines"
	if total == 1 {
		unit = "line"
	}
	return fmt.Sprintf("… (preview truncated, %d total %s)", total, unit)
}

func (w *Write) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[writeArguments](arguments)
	if err != nil {
		return errorResult(writeToolName, err), nil
	}
	if args.Content == nil {
		return errorResult(writeToolName, fmt.Errorf("content is required and must be a string")), nil
	}

	path, err := w.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(writeToolName, err), nil
	}

	info, statErr := os.Stat(path)
	if statErr == nil && !info.Mode().IsRegular() {
		return errorResult(writeToolName, fmt.Errorf("%s is not a regular file", w.workspace.display(path))), nil
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return errorResult(writeToolName, statErr), nil
	}
	if os.IsNotExist(statErr) {
		linkInfo, linkErr := os.Lstat(path)
		if linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			return errorResult(writeToolName, fmt.Errorf("%s is a dangling symlink", w.workspace.display(path))), nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return errorResult(writeToolName, err), nil
	}

	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	fileWriteErr := os.WriteFile(path, []byte(*args.Content), 0o666)
	if contextErr := ctx.Err(); contextErr != nil {
		result := errorResult(writeToolName, fmt.Errorf("canceled during non-transactional write; file may have changed: %w", contextErr))
		return result, contextErr
	}

	if fileWriteErr != nil {
		return errorResult(writeToolName, fileWriteErr), nil
	}

	return successResult(fmt.Sprintf("wrote %d bytes to %s", len(*args.Content), escapeOutputName(w.workspace.display(path)))), nil
}
