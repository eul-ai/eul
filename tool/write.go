package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"yaah/agent"
)

// Write creates or directly overwrites files. It follows filesystem symlinks
// and is intentionally not transactional.
type Write struct {
	workspace workspace
}

type writeArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

// NewWrite constructs a write tool rooted at cwd.
func NewWrite(cwd string) (*Write, error) {
	workspace, err := newWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	return &Write{workspace: workspace}, nil
}

func (w *Write) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:          "write",
		Description:   "Create or overwrite a regular file, creating parent directories. Symlinks to regular files are followed; dangling symlinks and special files are rejected. Existing file permissions are retained; new directories use 0777 and files use 0666, both subject to umask. Writes are not transactional.",
		PromptSummary: "Create or overwrite files and parent directories",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path":    {Type: "string", Description: "File path, relative to the session working directory or absolute."},
			"content": {Type: "string", Description: "Complete file content; an empty string writes an empty file."},
		}, "path", "content"),
	}
}

func (w *Write) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	if err := validateContext(ctx); err != nil {
		return agent.ToolResult{}, err
	}
	args, err := decodeArguments[writeArguments](arguments, "path", "content")
	if err != nil {
		return errorResult("write", err), nil
	}
	if args.Content == nil {
		return errorResult("write", fmt.Errorf("content is required and must be a string")), nil
	}
	path, err := w.workspace.resolve(args.Path)
	if err != nil {
		return errorResult("write", err), nil
	}

	if info, statErr := os.Stat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return errorResult("write", fmt.Errorf("%s is not a regular file", w.workspace.display(path))), nil
		}
	} else if !os.IsNotExist(statErr) {
		return errorResult("write", statErr), nil
	} else if linkInfo, linkErr := os.Lstat(path); linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 {
		return errorResult("write", fmt.Errorf("%s is a dangling symlink", w.workspace.display(path))), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return errorResult("write", err), nil
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	writeErr := os.WriteFile(path, []byte(*args.Content), 0o666)
	if contextErr := ctx.Err(); contextErr != nil {
		result := errorResult("write", fmt.Errorf("canceled during non-transactional write; file may have changed: %w", contextErr))
		return result, contextErr
	}
	if writeErr != nil {
		return errorResult("write", writeErr), nil
	}
	return successResult(fmt.Sprintf("wrote %d bytes to %s", len(*args.Content), escapeOutputName(w.workspace.display(path)))), nil
}
