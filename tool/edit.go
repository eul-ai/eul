package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
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

func NewEdit(cwd string) Tool {
	return spec[editArguments]{
		definition: editToolDefinition,
		present: func(args editArguments) agent.ToolPresentation {
			return editPresentation(args.Path)
		},
		run: newWorkspace(cwd).edit,
	}
}

func editPresentation(path string) agent.ToolPresentation {
	return agent.ToolPresentation{Title: editToolName, Arguments: displayToolArgument(path)}
}

func (w workspace) edit(ctx context.Context, args editArguments, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
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
	snapshot, err := textfile.Load(requestedPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("%s: %w", w.display(requestedPath), err)
	}
	original := snapshot.Data
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	oldBytes := []byte(*args.OldText)
	matches := bytes.Count(original, oldBytes)
	if matches == 0 {
		return agent.ToolResult{}, fmt.Errorf("old_text was not found in %s", w.display(requestedPath))
	}
	if matches > 1 && !args.All {
		return agent.ToolResult{}, fmt.Errorf("old_text occurs %d times in %s; expected exactly once when all is false", matches, w.display(requestedPath))
	}
	if *args.OldText == *args.NewText {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(w.display(requestedPath)))), nil
	}

	replacementCount := 1
	if args.All {
		replacementCount = -1
	}
	replacement := bytes.Replace(original, oldBytes, []byte(*args.NewText), replacementCount)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := textfile.Replace(snapshot, replacement); err != nil {
		return agent.ToolResult{}, err
	}
	presentation := editPresentation(args.Path)
	presentation.Diff = buildFileDiff(original, replacement)
	updates.SetFinal(presentation)
	return successResult(fmt.Sprintf("replaced text in %s", escapeOutputName(w.display(requestedPath)))), nil
}
