package tool

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/agent"
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
	bounded := truncateHead(text, defaultMaxLines, defaultMaxBytes)
	if notice == "" && !bounded.truncated {
		return text
	}
	if notice == "" {
		notice = "output truncated"
	}
	marker := "[" + notice + "]\n"
	if len(marker) >= defaultMaxBytes {
		marker, _ = truncateLine(marker, defaultMaxBytes)
		return marker
	}

	body := truncateHead(text, defaultMaxLines-1, defaultMaxBytes-len(marker)-1).text
	separator := ""
	if body != "" && !strings.HasSuffix(body, "\n") {
		separator = "\n"
	}
	return body + separator + marker
}

func boundTail(text, notice string) string {
	bounded := truncateTail(text, defaultMaxLines, defaultMaxBytes)
	if notice == "" && !bounded.truncated {
		return text
	}
	if notice == "" {
		notice = "earlier output truncated"
	}
	marker := "[" + notice + "]\n"
	if len(marker) >= defaultMaxBytes {
		marker, _ = truncateLine(marker, defaultMaxBytes)
		return marker
	}

	body := truncateTail(text, defaultMaxLines-1, defaultMaxBytes-len(marker)).text
	return marker + body
}

func escapeOutputName(name string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return replacer.Replace(name)
}

func snapshotString(snapshot PresentationSnapshot, name string) string {
	value, _ := snapshot.Arguments[name].(string)
	return value
}

func displayToolArgument(value string) string {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return strconv.Quote(value)
	}
	return value
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
