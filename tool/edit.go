package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
)

const editToolName = "edit"

var editToolDefinition = agent.ToolDefinition{
	Name:        editToolName,
	Description: "Edit a file by replacing one or all exact text matches.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"path":     {Type: "string", Description: "Relative or absolute file path."},
		"old_text": {Type: "string", Description: "Exact nonempty text to replace."},
		"new_text": {Type: "string", Description: "Replacement text; empty deletes matches."},
		"all":      {Type: "boolean", Description: "Replace all matches; false requires exactly one."},
	}, "path", "old_text", "new_text", "all"),
}

type Edit struct {
	workspace workspace
}

type editArguments struct {
	Path    string  `json:"path"`
	OldText *string `json:"old_text"`
	NewText *string `json:"new_text"`
	All     bool    `json:"all"`
}

func NewEdit(cwd string) *Edit {
	return &Edit{workspace: newWorkspace(cwd)}
}

func (*Edit) Definition() agent.ToolDefinition {
	return editToolDefinition
}

func (*Edit) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return editPresentation(snapshotString(snapshot, "path"))
}

func editPresentation(path string) agent.ToolPresentation {
	arguments := ""
	if path != "" {
		arguments = displayToolArgument(path)
	}
	return agent.ToolPresentation{Title: editToolName, Arguments: arguments}
}

func (e *Edit) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[editArguments](arguments)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	if args.OldText == nil || *args.OldText == "" {
		return errorResult(editToolName, fmt.Errorf("old_text is required and must be nonempty")), nil
	}
	if args.NewText == nil {
		return errorResult(editToolName, fmt.Errorf("new_text is required and must be a string")), nil
	}

	requestedPath, err := e.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	snapshot, err := textfile.Load(requestedPath)
	if err != nil {
		return errorResult(editToolName, fmt.Errorf("%s: %w", e.workspace.display(requestedPath), err)), nil
	}
	original := snapshot.Data
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	oldBytes := []byte(*args.OldText)
	matches := bytes.Count(original, oldBytes)
	if matches == 0 {
		return errorResult(editToolName, fmt.Errorf("old_text was not found in %s", e.workspace.display(requestedPath))), nil
	}
	if matches > 1 && !args.All {
		return errorResult(editToolName, fmt.Errorf("old_text occurs %d times in %s; expected exactly once when all is false", matches, e.workspace.display(requestedPath))), nil
	}
	if *args.OldText == *args.NewText {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(e.workspace.display(requestedPath)))), nil
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
		return errorResult(editToolName, err), nil
	}
	if updates != nil {
		presentation := editPresentation(args.Path)
		presentation.Diff = buildFileDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("replaced text in %s", escapeOutputName(e.workspace.display(requestedPath)))), nil
}
