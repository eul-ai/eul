package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/textfile"
)

const (
	insertToolName       = "insert"
	insertPositionBefore = "before"
	insertPositionAfter  = "after"
)

var insertToolDefinition = agent.ToolDefinition{
	Name:        insertToolName,
	Description: "Insert text before or after a unique anchor.",
	Parameters: StrictObject(map[string]agent.JSONSchema{
		"path":     {Type: "string", Description: "Relative or absolute file path."},
		"content":  {Type: "string", Description: "Exact text to insert; no newlines are added."},
		"anchor":   nullable("string", "Exact nonempty text that must occur once; null targets the file boundary."),
		"position": {Type: "string", Description: "before or after; with a null anchor, prepends or appends."},
	}, "path", "content", "anchor", "position"),
}

type Insert struct {
	workspace workspace
}

type insertArguments struct {
	Path     string  `json:"path"`
	Content  *string `json:"content"`
	Anchor   *string `json:"anchor"`
	Position string  `json:"position"`
}

func NewInsert(cwd string) *Insert {
	return &Insert{workspace: newWorkspace(cwd)}
}

func (*Insert) Definition() agent.ToolDefinition {
	return insertToolDefinition
}

func (*Insert) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	return insertPresentation(snapshotString(snapshot, "path"))
}

func insertPresentation(path string) agent.ToolPresentation {
	arguments := ""
	if path != "" {
		arguments = displayToolArgument(path)
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
	switch args.Position {
	case insertPositionBefore, insertPositionAfter:
	default:
		return errorResult(insertToolName, fmt.Errorf("position must be %q or %q", insertPositionBefore, insertPositionAfter)), nil
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

	offset, err := insertOffset(original, args.Anchor, args.Position)
	if err != nil {
		return errorResult(insertToolName, fmt.Errorf("%s: %w", i.workspace.display(requestedPath), err)), nil
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
		presentation := insertPresentation(args.Path)
		presentation.Diff = buildFileDiff(original, replacement)
		updates.SetFinal(presentation)
	}
	return successResult(fmt.Sprintf("inserted text in %s", escapeOutputName(i.workspace.display(requestedPath)))), nil
}

func insertOffset(content []byte, anchor *string, position string) (int, error) {
	if anchor == nil {
		if position == insertPositionBefore {
			return 0, nil
		}
		return len(content), nil
	}
	if *anchor == "" {
		return 0, fmt.Errorf("anchor must be nonempty")
	}

	anchorBytes := []byte(*anchor)
	matches := bytes.Count(content, anchorBytes)
	if matches == 0 {
		return 0, fmt.Errorf("anchor was not found")
	}
	if matches > 1 {
		return 0, fmt.Errorf("anchor occurs %d times; expected exactly once", matches)
	}

	offset := bytes.Index(content, anchorBytes)
	if position == insertPositionAfter {
		offset += len(anchorBytes)
	}
	return offset, nil
}
