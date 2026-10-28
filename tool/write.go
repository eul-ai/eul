package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eul-ai/eul/agent"
)

const (
	writeToolName             = "write"
	writePresentationMaxLines = 10
	writePresentationMaxBytes = 4 * 1024
)

var writeToolDefinition = agent.ToolDefinition{
	Name:        writeToolName,
	Description: "Create or overwrite a file, creating parent directories.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":    {Type: "string", Description: "Relative or absolute file path."},
		"content": {Type: "string", Description: "Complete file content."},
	}, "path", "content"),
}

type writeArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func NewWrite(cwd string) Tool {
	return spec[writeArguments]{
		definition: writeToolDefinition,
		present:    presentWrite,
		run:        newWorkspace(cwd).write,
	}
}

func presentWrite(args writeArguments) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Arguments: displayToolArgument(args.Path)}
	if args.Content != nil {
		presentation.Lines, presentation.LinesTruncated = writePreview(*args.Content)
	}
	return presentation
}

func writePreview(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}

	allLines := strings.Split(content, "\n")
	totalLines := len(allLines)
	if totalLines <= writePresentationMaxLines && len(content) <= writePresentationMaxBytes {
		return allLines, false
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
		bounded := prefixBytes(line, available)
		lines = append(lines, bounded)
		usedBytes += separatorBytes + len(bounded)
		if len(bounded) < len(line) {
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
	return lines, true
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

func (w workspace) write(ctx context.Context, args writeArguments, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if args.Content == nil {
		return agent.ToolResult{}, errors.New("content is required and must be a string")
	}

	path, err := w.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	fileMutation.Lock()
	defer fileMutation.Unlock()

	info, statErr := os.Stat(path)
	if statErr == nil && !info.Mode().IsRegular() {
		return agent.ToolResult{}, fmt.Errorf("%s is not a regular file", w.display(path))
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return agent.ToolResult{}, statErr
	}
	if os.IsNotExist(statErr) {
		linkInfo, linkErr := os.Lstat(path)
		if linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
			return agent.ToolResult{}, fmt.Errorf("%s is a dangling symlink", w.display(path))
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return agent.ToolResult{}, err
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
		return agent.ToolResult{}, fileWriteErr
	}

	return successResult(fmt.Sprintf("wrote %d bytes to %s", len(*args.Content), escapeOutputName(w.display(path)))), nil
}
