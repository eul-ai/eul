package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"yaah/agent"
	"yaah/tool/textfile"
)

const editToolName = "edit"

var editToolDefinition = agent.ToolDefinition{
	Name:        editToolName,
	Description: "Replace nonempty oldText exactly once in a UTF-8 text file. Read the file first when its exact contents are uncertain.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":    {Type: "string", Description: "File path, relative to the session working directory or absolute."},
		"oldText": {Type: "string", Description: "Nonempty exact text that must occur once."},
		"newText": {Type: "string", Description: "Replacement text, which may be empty."},
	}, "path", "oldText", "newText"),
}

type Edit struct {
	workspace workspace
}

type editArguments struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
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

	args, err := decodeArguments[editArguments](arguments)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	if args.OldText == nil || *args.OldText == "" {
		return errorResult(editToolName, fmt.Errorf("oldText is required and must be nonempty")), nil
	}
	if args.NewText == nil {
		return errorResult(editToolName, fmt.Errorf("newText is required and must be a string")), nil
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

	oldText := []byte(*args.OldText)
	matches := bytes.Count(original, oldText)
	if matches == 0 {
		return errorResult(editToolName, fmt.Errorf("oldText was not found in %s", e.workspace.display(requestedPath))), nil
	}
	if matches > 1 {
		return errorResult(editToolName, fmt.Errorf("oldText occurs %d times in %s; expected exactly once", matches, e.workspace.display(requestedPath))), nil
	}
	if *args.OldText == *args.NewText {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(e.workspace.display(requestedPath)))), nil
	}

	replacement := bytes.Replace(original, oldText, []byte(*args.NewText), 1)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := textfile.Replace(snapshot, replacement); err != nil {
		return errorResult(editToolName, err), nil
	}
	if updates != nil {
		presentation := editPresentation(args.Path)
		presentation.Diff = buildEditDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("edited %s", escapeOutputName(e.workspace.display(requestedPath)))), nil
}
