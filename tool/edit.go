package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"yaah/agent"
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

// Edit performs one exact replacement and commits it with a same-directory
// temporary file and rename. Final symlinks are resolved so the link is kept
// and its target is replaced.
type Edit struct {
	workspace workspace
}

type editArguments struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}

// NewEdit constructs an edit tool rooted at cwd.
func NewEdit(cwd string) *Edit {
	return &Edit{workspace: newWorkspace(cwd)}
}

func (*Edit) Definition() agent.ToolDefinition {
	return editToolDefinition
}

func (e *Edit) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
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
	targetPath, err := filepath.EvalSymlinks(requestedPath)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult(editToolName, fmt.Errorf("%s is not a regular file", e.workspace.display(requestedPath))), nil
	}

	original, err := os.ReadFile(targetPath)
	if err != nil {
		return errorResult(editToolName, err), nil
	}
	if err := validateText(original); err != nil {
		return errorResult(editToolName, fmt.Errorf("%s: %w", e.workspace.display(requestedPath), err)), nil
	}
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
	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".yaah-edit-*")
	if err != nil {
		return errorResult(editToolName, err), nil
	}

	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(info.Mode()); err != nil {
		return errorResult(editToolName, err), nil
	}
	if _, err := temporary.Write(replacement); err != nil {
		return errorResult(editToolName, err), nil
	}
	if err := temporary.Close(); err != nil {
		return errorResult(editToolName, err), nil
	}

	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return errorResult(editToolName, err), nil
	}

	committed = true
	return successResult(fmt.Sprintf("edited %s", escapeOutputName(e.workspace.display(requestedPath)))), nil
}
