package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
)

const editToolName = "edit"

var editToolDefinition = agent.ToolDefinition{
	Name:        editToolName,
	Description: "Edit a file by replacing one or all exact text matches.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":     {Type: "string", Description: "Relative or absolute file path."},
		"old_text": {Type: "string", Description: "Exact nonempty text to replace."},
		"new_text": {Type: "string", Description: "Replacement text; empty deletes matches."},
		"all":      {Type: "boolean", Description: "Replace all matches; false requires exactly one."},
	}, "path", "old_text", "new_text", "all"),
}

type editArguments struct {
	Path    string  `json:"path"`
	OldText *string `json:"old_text"`
	NewText *string `json:"new_text"`
	All     bool    `json:"all"`
}

func NewEdit(cwd string) agent.Tool {
	return spec[editArguments]{
		definition: editToolDefinition,
		present: func(args editArguments) agent.ToolPresentation {
			return editPresentation(args.Path)
		},
		run: newWorkspace(cwd).edit,
	}
}

func editPresentation(path string) agent.ToolPresentation {
	return agent.ToolPresentation{Arguments: displayToolArgument(path)}
}

func (w workspace) edit(ctx context.Context, args editArguments, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if args.OldText == nil || *args.OldText == "" {
		return agent.ToolResult{}, errors.New("old_text is required and must be nonempty")
	}
	if args.NewText == nil {
		return agent.ToolResult{}, errors.New("new_text is required and must be a string")
	}

	requestedPath, err := w.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	fileMutation.Lock()
	defer fileMutation.Unlock()

	snapshot, err := loadText(requestedPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("%s: %w", w.display(requestedPath), err)
	}
	original := snapshot.data

	oldBytes := []byte(*args.OldText)
	newBytes := []byte(*args.NewText)
	starts := matchStarts(original, oldBytes)
	if len(starts) == 0 {
		return agent.ToolResult{}, fmt.Errorf("old_text was not found in %s", w.display(requestedPath))
	}
	if len(starts) > 1 && !args.All {
		return agent.ToolResult{}, fmt.Errorf("old_text occurs %d times in %s; expected exactly once when all is false", len(starts), w.display(requestedPath))
	}
	if *args.OldText == *args.NewText {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(w.display(requestedPath)))), nil
	}

	replacement := bytes.ReplaceAll(original, oldBytes, newBytes)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := replaceText(snapshot, replacement); err != nil {
		return agent.ToolResult{}, err
	}
	presentation := editPresentation(args.Path)
	presentation.Diff = buildFileDiff(original, replacement, starts, len(oldBytes), len(newBytes))
	return presentedResult(fmt.Sprintf("replaced text in %s", escapeOutputName(w.display(requestedPath))), presentation), nil
}

func matchStarts(data, pattern []byte) []int {
	var starts []int
	for offset := 0; ; {
		index := bytes.Index(data[offset:], pattern)
		if index < 0 {
			return starts
		}
		starts = append(starts, offset+index)
		offset += index + len(pattern)
	}
}
