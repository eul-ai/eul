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

const readToolName = "read"

var readToolDefinition = agent.ToolDefinition{
	Name:        readToolName,
	Description: "Read a regular UTF-8 text file by path and optional line range with bounded output.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":   {Type: "string", Description: "File path, relative to the session working directory or absolute."},
		"offset": nullable("integer", "Optional one-based starting line; null defaults to 1."),
		"limit":  nullable("integer", "Optional maximum lines; null defaults to 2000."),
	}, "path", "offset", "limit"),
}

type Read struct {
	workspace workspace
}

type readArguments struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

func NewRead(cwd string) *Read {
	return &Read{workspace: newWorkspace(cwd)}
}

func (*Read) Definition() agent.ToolDefinition {
	return readToolDefinition
}

func (*Read) Presentation(snapshot PresentationSnapshot) agent.ToolPresentation {
	arguments := ""
	if path := snapshotString(snapshot, "path"); path != "" {
		arguments = displayToolArgument(path)
	}

	offset, hasOffset := int64(0), false
	if number, ok := snapshot.Arguments["offset"].(json.Number); ok {
		if value, err := number.Int64(); err == nil {
			offset, hasOffset = value, true
		}
	}
	limit, hasLimit := int64(0), false
	if number, ok := snapshot.Arguments["limit"].(json.Number); ok {
		if value, err := number.Int64(); err == nil {
			limit, hasLimit = value, true
		}
	}
	if hasOffset || hasLimit {
		start := int64(1)
		if hasOffset {
			start = offset
		}
		arguments += fmt.Sprintf(":%d", start)
		if hasLimit {
			arguments += fmt.Sprintf("-%d", start+limit-1)
		}
	}

	return agent.ToolPresentation{Title: readToolName, Arguments: arguments}
}

func (r *Read) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeArguments[readArguments](arguments)
	if err != nil {
		return errorResult(readToolName, err), nil
	}
	offset, err := optionalPositive(args.Offset, 1, int(^uint(0)>>1), "offset")
	if err != nil {
		return errorResult(readToolName, err), nil
	}
	limit, err := optionalPositive(args.Limit, defaultMaxLines, defaultMaxLines, "limit")
	if err != nil {
		return errorResult(readToolName, err), nil
	}

	path, err := r.workspace.resolve(args.Path)
	if err != nil {
		return errorResult(readToolName, err), nil
	}

	// Opening a FIFO or device can block, so reject nonregular paths before
	// os.Open and recheck the descriptor after opening to catch path replacement.
	info, err := os.Stat(path)
	if err != nil {
		return errorResult(readToolName, err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult(readToolName, fmt.Errorf("%s is not a regular file", r.workspace.display(path))), nil
	}

	file, err := os.Open(path)
	if err != nil {
		return errorResult(readToolName, err), nil
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return errorResult(readToolName, err), nil
	}
	if !openedInfo.Mode().IsRegular() {
		return errorResult(readToolName, fmt.Errorf("%s is not a regular file", r.workspace.display(path))), nil
	}

	output, err := scanReadRange(ctx, file, offset, limit)
	if err != nil {
		var contextErr *readContextError
		switch {
		case errors.As(err, &contextErr):
			return agent.ToolResult{}, contextErr.cause
		case errors.Is(err, errReadBinary):
			return errorResult(readToolName, fmt.Errorf("%s: binary file is not supported", r.workspace.display(path))), nil
		default:
			return errorResult(readToolName, err), nil
		}
	}
	return successResult(output), nil
}

var errReadBinary = errors.New("binary file is not supported")

type readContextError struct {
	cause error
}

func (err *readContextError) Error() string {
	return err.cause.Error()
}

func (err *readContextError) Unwrap() error {
	return err.cause
}

func scanReadRange(ctx context.Context, source io.Reader, offset, limit int) (string, error) {
	reader := bufio.NewReader(source)
	var output strings.Builder
	lineNumber := 1
	selectedCompleted := 0
	lineEnds := make([]int, 0, min(limit, defaultMaxLines))
	sawData := false
	lastWasNewline := false
	truncatedOutput := ""

	for {
		if err := ctx.Err(); err != nil {
			return "", &readContextError{cause: err}
		}

		runeValue, size, readErr := reader.ReadRune()
		if errors.Is(readErr, io.EOF) {
			if truncatedOutput != "" {
				return truncatedOutput, nil
			}
			if !sawData {
				if offset != 1 {
					return "", fmt.Errorf("offset %d is beyond end of empty file", offset)
				}
				return "", nil
			}

			lineCount := lineNumber
			if lastWasNewline {
				lineCount--
			}
			if offset > lineCount {
				return "", fmt.Errorf("offset %d is beyond end of file (%d lines)", offset, lineCount)
			}
			return output.String(), nil
		}
		if readErr != nil {
			return "", readErr
		}
		if runeValue == utf8.RuneError && size == 1 || runeValue == 0 {
			return "", errReadBinary
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
		if output.Len()+size > defaultMaxBytes {
			truncatedOutput = formatReadByteTruncation(output.String(), lineEnds, offset, lineNumber)
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

func formatReadByteTruncation(text string, lineEnds []int, offset, lineNumber int) string {
	if len(lineEnds) == 0 {
		return boundHead(text, fmt.Sprintf("truncated within line %d; no lossless next offset", lineNumber))
	}

	return formatReadTruncation(text[:lineEnds[len(lineEnds)-1]], lineEnds, offset)
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
		if keptLines <= defaultMaxLines-1 && end <= defaultMaxBytes-markerBytes-1 {
			return boundHead(text[:end], notice)
		}
		keptLines--
	}
}
