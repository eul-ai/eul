package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"yaah/agent"
)

const (
	maxOutputLines = 2_000
	maxOutputBytes = 50 * 1024
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

func strictObject(properties map[string]agent.JSONSchema, required ...string) agent.JSONSchema {
	additionalProperties := false
	return agent.JSONSchema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: &additionalProperties,
	}
}

func decodeArguments[T any](arguments json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("tool: decode arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("tool: arguments contain multiple JSON values")
		}
		return value, fmt.Errorf("tool: decode trailing arguments: %w", err)
	}
	return value, nil
}

func errorResult(name string, err error) agent.ToolResult {
	return agent.ToolResult{Output: boundHead(fmt.Sprintf("%s: %v", name, err)), IsError: true}
}

func successResult(output string) agent.ToolResult {
	return agent.ToolResult{Output: boundHead(output)}
}

func boundHead(text string) string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	truncated := len(lines) > maxOutputLines
	if truncated {
		lines = lines[:maxOutputLines]
	}
	body := strings.Join(lines, "")
	if len(body) > maxOutputBytes {
		body = prefixUTF8(body, maxOutputBytes)
		truncated = true
	}
	if !truncated {
		return text
	}
	const marker = "[output truncated]\n"
	body = prefixUTF8(body, maxOutputBytes-len(marker)-1)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + marker
}

func prefixUTF8(text string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(text) <= maximum {
		return text
	}
	end := maximum
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
