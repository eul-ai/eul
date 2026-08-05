package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"yaah/agent"
)

// Read reads bounded UTF-8 text from regular files relative to a fixed working
// directory. Absolute paths and paths containing .. are intentionally allowed.
type Read struct {
	workspace workspace
}

type readArguments struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

// NewRead constructs a read tool rooted at cwd.
func NewRead(cwd string) (*Read, error) {
	workspace, err := newWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	return &Read{workspace: workspace}, nil
}

func (r *Read) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:          "read",
		Description:   "Read a regular UTF-8 text file with one-based line offsets and bounded output. Symlinks to regular files are followed; directories, special files, NUL bytes, and invalid UTF-8 are rejected.",
		PromptSummary: "Read text files by path and optional line range",
		Parameters: strictObject(map[string]agent.JSONSchema{
			"path":   {Type: "string", Description: "File path, relative to the session working directory or absolute."},
			"offset": nullable("integer", "Optional one-based starting line; null defaults to 1."),
			"limit":  nullable("integer", "Optional maximum lines; null defaults to 2000."),
		}, "path", "offset", "limit"),
	}
}

func (r *Read) Execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	if err := validateContext(ctx); err != nil {
		return agent.ToolResult{}, err
	}
	args, err := decodeArguments[readArguments](arguments, "path", "offset", "limit")
	if err != nil {
		return errorResult("read", err), nil
	}
	offset, err := optionalPositive(args.Offset, 1, int(^uint(0)>>1), "offset")
	if err != nil {
		return errorResult("read", err), nil
	}
	limit, err := optionalPositive(args.Limit, DefaultMaxLines, DefaultMaxLines, "limit")
	if err != nil {
		return errorResult("read", err), nil
	}
	path, err := r.workspace.resolve(args.Path)
	if err != nil {
		return errorResult("read", err), nil
	}

	// Stat before opening so FIFOs and devices fail instead of blocking in
	// os.Open. The second check on the opened descriptor catches ordinary path
	// replacement races; path handling is not a security boundary.
	info, err := os.Stat(path)
	if err != nil {
		return errorResult("read", err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult("read", fmt.Errorf("%s is not a regular file", r.workspace.display(path))), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return errorResult("read", err), nil
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return errorResult("read", err), nil
	}
	if !openedInfo.Mode().IsRegular() {
		return errorResult("read", fmt.Errorf("%s is not a regular file", r.workspace.display(path))), nil
	}

	reader := bufio.NewReader(file)
	var output strings.Builder
	lineNumber := 1
	selectedCompleted := 0
	lineEnds := make([]int, 0, min(limit, DefaultMaxLines))
	sawData := false
	lastWasNewline := false
	truncatedOutput := ""

	for {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		runeValue, size, readErr := reader.ReadRune()
		if errors.Is(readErr, io.EOF) {
			if truncatedOutput != "" {
				return agent.ToolResult{Output: truncatedOutput}, nil
			}
			if !sawData {
				if offset != 1 {
					return errorResult("read", fmt.Errorf("offset %d is beyond end of empty file", offset)), nil
				}
				return successResult(""), nil
			}
			lineCount := lineNumber
			if lastWasNewline {
				lineCount--
			}
			if offset > lineCount {
				return errorResult("read", fmt.Errorf("offset %d is beyond end of file (%d lines)", offset, lineCount)), nil
			}
			return agent.ToolResult{Output: output.String()}, nil
		}
		if readErr != nil {
			return errorResult("read", readErr), nil
		}
		if runeValue == utf8.RuneError && size == 1 || runeValue == 0 {
			return errorResult("read", fmt.Errorf("%s: binary file is not supported", r.workspace.display(path))), nil
		}

		sawData = true
		lastWasNewline = runeValue == '\n'
		if truncatedOutput != "" {
			if runeValue == '\n' {
				lineNumber++
			}
			continue
		}
		if lineNumber < offset {
			if runeValue == '\n' {
				lineNumber++
			}
			continue
		}
		if selectedCompleted >= limit {
			truncatedOutput = formatReadTruncation(output.String(), lineEnds, offset)
			if runeValue == '\n' {
				lineNumber++
			}
			continue
		}
		if output.Len()+size > DefaultMaxBytes {
			if len(lineEnds) == 0 {
				truncatedOutput = boundHead(output.String(), fmt.Sprintf("truncated within line %d; no lossless next offset", lineNumber))
			} else {
				completeOutput := output.String()[:lineEnds[len(lineEnds)-1]]
				truncatedOutput = formatReadTruncation(completeOutput, lineEnds, offset)
			}
			if runeValue == '\n' {
				lineNumber++
			}
			continue
		}
		output.WriteRune(runeValue)
		if runeValue == '\n' {
			selectedCompleted++
			lineEnds = append(lineEnds, output.Len())
			lineNumber++
		}
	}
}

func formatReadTruncation(text string, lineEnds []int, offset int) string {
	keptLines := len(lineEnds)
	for {
		nextOffset := offset + keptLines
		notice := fmt.Sprintf("truncated; next offset: %d", nextOffset)
		markerBytes := len(notice) + len("[]\n")
		end := 0
		if keptLines > 0 {
			end = lineEnds[keptLines-1]
		}
		if keptLines <= DefaultMaxLines-1 && end <= DefaultMaxBytes-markerBytes-1 {
			return boundHead(text[:end], notice)
		}
		keptLines--
	}
}
