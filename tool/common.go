package tool

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"yaah/agent"
)

type workspace struct {
	cwd string
}

func newWorkspace(cwd string) workspace {
	return workspace{cwd: cwd}
}

func (w workspace) resolve(name string) (string, error) {
	if name == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	return filepath.Join(w.cwd, name), nil
}

func (w workspace) display(name string) string {
	name = filepath.Clean(name)
	relative, err := filepath.Rel(w.cwd, name)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(name)
}

func strictObject(properties map[string]agent.JSONSchema, required ...string) agent.JSONSchema {
	additionalProperties := false
	return agent.JSONSchema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: &additionalProperties,
	}
}

func nullable(schemaType string, description string) agent.JSONSchema {
	return agent.JSONSchema{
		Description: description,
		AnyOf: []agent.JSONSchema{
			{Type: schemaType},
			{Type: "null"},
		},
	}
}

func errorResult(toolName string, err error) agent.ToolResult {
	return agent.ToolResult{
		Output:  boundHead(fmt.Sprintf("%s: %v", toolName, err), ""),
		IsError: true,
	}
}

func successResult(output string) agent.ToolResult {
	return agent.ToolResult{Output: boundHead(output, "")}
}

func boundHead(text, notice string) string {
	bounded := TruncateHead(text, DefaultMaxLines, DefaultMaxBytes)
	if notice == "" && !bounded.Truncated {
		return text
	}
	if notice == "" {
		notice = "output truncated"
	}
	marker := "[" + notice + "]\n"
	if len(marker) >= DefaultMaxBytes {
		marker, _ = TruncateLine(marker, DefaultMaxBytes)
		return marker
	}

	body := TruncateHead(text, DefaultMaxLines-1, DefaultMaxBytes-len(marker)-1).Text
	separator := ""
	if body != "" && !strings.HasSuffix(body, "\n") {
		separator = "\n"
	}
	return body + separator + marker
}

func boundTail(text, notice string) string {
	bounded := TruncateTail(text, DefaultMaxLines, DefaultMaxBytes)
	if notice == "" && !bounded.Truncated {
		return text
	}
	if notice == "" {
		notice = "earlier output truncated"
	}
	marker := "[" + notice + "]\n"
	if len(marker) >= DefaultMaxBytes {
		marker, _ = TruncateLine(marker, DefaultMaxBytes)
		return marker
	}
	body := TruncateTail(text, DefaultMaxLines-1, DefaultMaxBytes-len(marker)).Text
	return marker + body
}

func validateText(data []byte) error {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("binary file is not supported")
	}
	return nil
}

func splitTextLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func escapeOutputName(name string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return replacer.Replace(name)
}

func optionalPositive(value *int, defaultValue, maximum int, field string) (int, error) {
	if value == nil {
		return defaultValue, nil
	}
	if *value <= 0 {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	if *value > maximum {
		return 0, fmt.Errorf("%s must not exceed %d", field, maximum)
	}
	return *value, nil
}
