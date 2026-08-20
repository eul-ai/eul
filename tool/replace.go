package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
)

const replaceToolName = "replace"

var replaceToolDefinition = agent.ToolDefinition{
	Name:        replaceToolName,
	Description: "Replace one or all exact text matches.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"path":    {Type: "string", Description: "Relative or absolute file path."},
		"oldText": {Type: "string", Description: "Exact nonempty text to replace."},
		"newText": {Type: "string", Description: "Replacement text; empty deletes matches."},
		"all":     {Type: "boolean", Description: "Replace all matches; false requires exactly one."},
	}, "path", "oldText", "newText", "all"),
}

type Replace struct {
	workspace workspace
}

type replaceArguments struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
	All     bool    `json:"all"`
}

func NewReplace(cwd string) *Replace {
	return &Replace{workspace: newWorkspace(cwd)}
}

func (*Replace) Definition() agent.ToolDefinition {
	return replaceToolDefinition
}

func (*Replace) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return replacePresentation(snapshotString(snapshot, "path"))
}

func replacePresentation(path string) agent.ToolPresentation {
	arguments := ""
	if path != "" {
		arguments = displayToolArgument(path)
	}
	return agent.ToolPresentation{Title: replaceToolName, Arguments: arguments}
}

func (r *Replace) Execute(ctx context.Context, arguments json.RawMessage, updates agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := DecodeArguments[replaceArguments](arguments)
	if err != nil {
		return errorResult(replaceToolName, err), nil
	}
	if args.OldText == nil || *args.OldText == "" {
		return errorResult(replaceToolName, fmt.Errorf("oldText is required and must be nonempty")), nil
	}
	if args.NewText == nil {
		return errorResult(replaceToolName, fmt.Errorf("newText is required and must be a string")), nil
	}

	requestedPath, err := r.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(replaceToolName, err), nil
	}
	snapshot, err := textfile.Load(requestedPath)
	if err != nil {
		return errorResult(replaceToolName, fmt.Errorf("%s: %w", r.workspace.display(requestedPath), err)), nil
	}
	original := snapshot.Data
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	oldText := []byte(*args.OldText)
	matches := bytes.Count(original, oldText)
	if matches == 0 {
		return errorResult(replaceToolName, fmt.Errorf("oldText was not found in %s", r.workspace.display(requestedPath))), nil
	}
	if matches > 1 && !args.All {
		return errorResult(replaceToolName, fmt.Errorf("oldText occurs %d times in %s; expected exactly once when all is false", matches, r.workspace.display(requestedPath))), nil
	}
	if *args.OldText == *args.NewText {
		return successResult(fmt.Sprintf("no changes needed in %s", escapeOutputName(r.workspace.display(requestedPath)))), nil
	}

	replacementCount := 1
	if args.All {
		replacementCount = -1
	}
	replacement := bytes.Replace(original, oldText, []byte(*args.NewText), replacementCount)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := textfile.Replace(snapshot, replacement); err != nil {
		return errorResult(replaceToolName, err), nil
	}
	if updates != nil {
		presentation := replacePresentation(args.Path)
		presentation.Diff = buildFileDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("replaced text in %s", escapeOutputName(r.workspace.display(requestedPath)))), nil
}
