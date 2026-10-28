package tool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/eul-ai/eul/agent"
)

const readToolName = "read"

var readToolDefinition = agent.ToolDefinition{
	Name:        readToolName,
	Description: "Read all or part of a text file.",
	Parameters: strictObject(map[string]agent.JSONSchema{
		"path":   {Type: "string", Description: "Relative or absolute file path."},
		"offset": nullable("integer", "Starting line; null uses line 1."),
		"limit":  nullable("integer", "Maximum lines; null uses 2000."),
	}, "path", "offset", "limit"),
}

type readArguments struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

func NewRead(cwd string) Tool {
	return spec[readArguments]{
		definition: readToolDefinition,
		present:    presentRead,
		run:        newWorkspace(cwd).read,
	}
}

func presentRead(args readArguments) agent.ToolPresentation {
	arguments := displayToolArgument(args.Path)
	if args.Offset != nil || args.Limit != nil {
		start := 1
		if args.Offset != nil {
			start = *args.Offset
		}
		arguments += fmt.Sprintf(":%d", start)
		if args.Limit != nil {
			arguments += fmt.Sprintf("-%d", start+*args.Limit-1)
		}
	}
	return agent.ToolPresentation{Arguments: arguments}
}

func (w workspace) read(ctx context.Context, args readArguments, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	offset, err := optionalPositive(args.Offset, 1, math.MaxInt, "offset")
	if err != nil {
		return agent.ToolResult{}, err
	}
	limit, err := optionalPositive(args.Limit, defaultMaxLines, defaultMaxLines, "limit")
	if err != nil {
		return agent.ToolResult{}, err
	}

	path, err := w.resolve(args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	// Opening a FIFO or device can block, so reject nonregular paths first.
	info, err := os.Stat(path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !info.Mode().IsRegular() {
		return agent.ToolResult{}, fmt.Errorf("%s is not a regular file", w.display(path))
	}

	file, err := os.Open(path)
	if err != nil {
		return agent.ToolResult{}, err
	}
	defer file.Close()

	output, err := scanReadRange(ctx, file, offset, limit)
	switch {
	case errors.Is(err, errReadBinary):
		return agent.ToolResult{}, fmt.Errorf("%s: binary file is not supported", w.display(path))
	case err != nil:
		return agent.ToolResult{}, err
	}
	return successResult(output), nil
}

var errReadBinary = errors.New("binary file is not supported")

func scanReadRange(ctx context.Context, source io.Reader, offset, limit int) (string, error) {
	reader := bufio.NewReader(source)
	var output bytes.Buffer
	var lineEnds []int
	lineNumber := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if len(line) == 0 {
			break
		}
		lineNumber++

		selected := lineNumber >= offset
		overflow := output.Len()+len(line) > defaultMaxBytes
		if selected && (len(lineEnds) == limit || overflow && len(lineEnds) > 0) {
			return formatReadTruncation(output.String(), lineEnds, offset), nil
		}
		if isBinary(line) {
			return "", errReadBinary
		}
		switch {
		case !selected:
		case overflow:
			return bound(string(line), fmt.Sprintf("truncated within line %d; no lossless next offset", lineNumber), false), nil
		default:
			output.Write(line)
			lineEnds = append(lineEnds, output.Len())
		}
	}

	switch {
	case offset == 1 || offset <= lineNumber:
		return output.String(), nil
	case lineNumber == 0:
		return "", fmt.Errorf("offset %d is beyond end of empty file", offset)
	default:
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines)", offset, lineNumber)
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
		if keptLines <= defaultMaxLines-1 && end <= defaultMaxBytes-markerBytes-1 {
			return bound(text[:end], notice, false)
		}
		keptLines--
	}
}
