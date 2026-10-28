package tool

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
)

const (
	defaultMaxLines = 2_000
	defaultMaxBytes = 50 * 1024
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
		Type:        []string{schemaType, "null"},
		Description: description,
	}
}

func errorResult(toolName string, err error) agent.ToolResult {
	return agent.ToolResult{
		Output:  bound(fmt.Sprintf("%s: %v", toolName, err), "", false),
		IsError: true,
	}
}

func successResult(output string) agent.ToolResult {
	return agent.ToolResult{Output: bound(output, "", false)}
}

func presentedResult(output string, presentation agent.ToolPresentation) agent.ToolResult {
	result := successResult(output)
	result.Presentation = &presentation
	return result
}

// bound limits text to defaultMaxLines and defaultMaxBytes, keeping its head or
// tail. A nonempty notice, or any truncation, adds a bracketed marker line.
func bound(text, notice string, keepTail bool) string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if notice == "" && len(lines) <= defaultMaxLines && len(text) <= defaultMaxBytes {
		return text
	}

	if notice == "" {
		notice = "output truncated"
		if keepTail {
			notice = "earlier output truncated"
		}
	}
	marker := "[" + notice + "]\n"
	if keepTail {
		lines = lines[max(0, len(lines)-defaultMaxLines+1):]
		return marker + suffixBytes(strings.Join(lines, ""), defaultMaxBytes-len(marker))
	}

	lines = lines[:min(len(lines), defaultMaxLines-1)]
	body := prefixBytes(strings.Join(lines, ""), defaultMaxBytes-len(marker)-1)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + marker
}

func prefixBytes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}

	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func suffixBytes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}

	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func escapeOutputName(name string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return replacer.Replace(name)
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
		return 0, fmt.Errorf("%s must not exceed %d (requested: %d)", field, maximum, *value)
	}
	return *value, nil
}
