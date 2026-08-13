package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.lsp.dev/protocol"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool"
)

const (
	lspDiagnosticsToolName = "lsp_diagnostics"
	lspHoverToolName       = "lsp_hover"
	lspDefinitionToolName  = "lsp_definition"
	lspReferencesToolName  = "lsp_references"
	lspSymbolsToolName     = "lsp_symbols"
	lspRenameToolName      = "lsp_rename"
	lspMaxOutputLines      = 2_000
	lspMaxOutputBytes      = 50 * 1024
)

var (
	lspDiagnosticsToolDefinition = agent.ToolDefinition{
		Name:        lspDiagnosticsToolName,
		Description: "Return current language-server diagnostics for a source file.",
		Parameters: tool.StrictObject(map[string]agent.JSONSchema{
			"path": {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		}, "path"),
	}
	lspHoverToolDefinition = agent.ToolDefinition{
		Name:        lspHoverToolName,
		Description: "Return type and documentation information from the language server at a source position.",
		Parameters:  lspPositionSchema(),
	}
	lspDefinitionToolDefinition = agent.ToolDefinition{
		Name:        lspDefinitionToolName,
		Description: "Return language-server definition locations for the symbol at a source position.",
		Parameters:  lspPositionSchema(),
	}
	lspReferencesToolDefinition = agent.ToolDefinition{
		Name:        lspReferencesToolName,
		Description: "Return language-server reference locations for the symbol at a source position.",
		Parameters: tool.StrictObject(map[string]agent.JSONSchema{
			"path":                {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
			"line":                {Type: "integer", Description: "Zero-based line number."},
			"character":           {Type: "integer", Description: "Zero-based UTF-16 character offset."},
			"include_declaration": {Type: "boolean", Description: "Whether to include the symbol declaration; defaults to false."},
		}, "path", "line", "character"),
	}
	lspSymbolsToolDefinition = agent.ToolDefinition{
		Name:        lspSymbolsToolName,
		Description: "Return language-server document symbols for a source file.",
		Parameters: tool.StrictObject(map[string]agent.JSONSchema{
			"path": {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		}, "path"),
	}
	lspRenameToolDefinition = agent.ToolDefinition{
		Name:        lspRenameToolName,
		Description: "Rename the symbol at a source position and apply the language-server workspace edits.",
		Parameters: tool.StrictObject(map[string]agent.JSONSchema{
			"path":      {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
			"line":      {Type: "integer", Description: "Approximate zero-based line used to disambiguate old_name."},
			"character": {Type: "integer", Description: "Approximate zero-based UTF-16 character offset used to disambiguate old_name."},
			"old_name":  {Type: "string", Description: "Current symbol name."},
			"new_name":  {Type: "string", Description: "New symbol name."},
		}, "path", "line", "character", "old_name", "new_name"),
	}
)

type lspOperation uint8

const (
	lspDiagnostics lspOperation = iota
	lspHover
	lspDefinition
	lspReferences
	lspSymbols
	lspRename
)

type lspTool struct {
	service    *service
	definition agent.ToolDefinition
	operation  lspOperation
}

type lspPathArguments struct {
	Path string `json:"path"`
}

type lspPositionArguments struct {
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Character *int   `json:"character"`
}

type lspReferencesArguments struct {
	Path               string `json:"path"`
	Line               *int   `json:"line"`
	Character          *int   `json:"character"`
	IncludeDeclaration bool   `json:"include_declaration"`
}

type lspRenameArguments struct {
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Character *int   `json:"character"`
	OldName   string `json:"old_name"`
	NewName   string `json:"new_name"`
}

func newTools(service *service, includeRename bool) []tool.Tool {
	if !service.available() {
		return nil
	}
	tools := []tool.Tool{
		&lspTool{service: service, definition: lspDiagnosticsToolDefinition, operation: lspDiagnostics},
		&lspTool{service: service, definition: lspHoverToolDefinition, operation: lspHover},
		&lspTool{service: service, definition: lspDefinitionToolDefinition, operation: lspDefinition},
		&lspTool{service: service, definition: lspReferencesToolDefinition, operation: lspReferences},
		&lspTool{service: service, definition: lspSymbolsToolDefinition, operation: lspSymbols},
	}
	if includeRename {
		tools = append(tools, &lspTool{service: service, definition: lspRenameToolDefinition, operation: lspRename})
	}
	return tools
}

func (t *lspTool) Definition() agent.ToolDefinition {
	return t.definition
}

func (t *lspTool) Presentation(snapshot tool.PresentationSnapshot) agent.ToolPresentation {
	presentation := agent.ToolPresentation{Title: t.definition.Name}
	switch t.operation {
	case lspDiagnostics:
		presentation.Arguments, _ = snapshot.Arguments["path"].(string)
	case lspRename:
		old_name, _ := snapshot.Arguments["old_name"].(string)
		new_name, _ := snapshot.Arguments["new_name"].(string)
		if old_name != "" && new_name != "" {
			presentation.Arguments = old_name + " → " + new_name
		}
	}
	return presentation
}

func (t *lspTool) Execute(ctx context.Context, arguments json.RawMessage, _ agent.ToolUpdateSink) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	result, err := t.execute(ctx, arguments)
	if err == nil {
		return result, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return agent.ToolResult{}, contextErr
	}
	return lspErrorResult(t.definition.Name, err), nil
}

func (t *lspTool) execute(ctx context.Context, arguments json.RawMessage) (agent.ToolResult, error) {
	switch t.operation {
	case lspDiagnostics, lspSymbols:
		args, err := tool.DecodeArguments[lspPathArguments](arguments)
		if err != nil {
			return agent.ToolResult{}, err
		}
		var response any
		switch t.operation {
		case lspDiagnostics:
			response, err = t.service.diagnostics(ctx, args.Path)
		case lspSymbols:
			response, err = t.service.symbols(ctx, args.Path)
		}
		if err != nil {
			return agent.ToolResult{}, err
		}
		return formatLSPResult(response)
	case lspHover, lspDefinition:
		args, err := tool.DecodeArguments[lspPositionArguments](arguments)
		if err != nil {
			return agent.ToolResult{}, err
		}
		line, character, err := lspPosition(args.Line, args.Character)
		if err != nil {
			return agent.ToolResult{}, err
		}
		var response any
		switch t.operation {
		case lspHover:
			response, err = t.service.hover(ctx, args.Path, line, character)
		case lspDefinition:
			response, err = t.service.definition(ctx, args.Path, line, character)
		}
		if err != nil {
			return agent.ToolResult{}, err
		}
		return formatLSPResult(response)
	case lspReferences:
		args, err := tool.DecodeArguments[lspReferencesArguments](arguments)
		if err != nil {
			return agent.ToolResult{}, err
		}
		line, character, err := lspPosition(args.Line, args.Character)
		if err != nil {
			return agent.ToolResult{}, err
		}
		response, err := t.service.references(ctx, args.Path, line, character, args.IncludeDeclaration)
		if err != nil {
			return agent.ToolResult{}, err
		}
		return formatLSPResult(response)
	case lspRename:
		args, err := tool.DecodeArguments[lspRenameArguments](arguments)
		if err != nil {
			return agent.ToolResult{}, err
		}
		line, character, err := lspPosition(args.Line, args.Character)
		if err != nil {
			return agent.ToolResult{}, err
		}
		changed, err := t.service.renameSymbol(ctx, args.Path, line, character, args.OldName, args.NewName)
		if err != nil {
			return agent.ToolResult{}, err
		}
		return lspSuccessResult(fmt.Sprintf("renamed symbol in %d files", changed)), nil
	default:
		return agent.ToolResult{}, errors.New("unknown LSP operation")
	}
}

func lspPosition(line, character *int) (int, int, error) {
	if line == nil || *line < 0 {
		return 0, 0, errors.New("line is required and must be nonnegative")
	}
	if character == nil || *character < 0 {
		return 0, 0, errors.New("character is required and must be nonnegative")
	}
	return *line, *character, nil
}

func lspPositionSchema() agent.JSONSchema {
	return tool.StrictObject(map[string]agent.JSONSchema{
		"path":      {Type: "string", Description: "Source file path, relative to the session working directory or absolute."},
		"line":      {Type: "integer", Description: "Zero-based line number."},
		"character": {Type: "integer", Description: "Zero-based UTF-16 character offset."},
	}, "path", "line", "character")
}

func formatLSPResult(response any) (agent.ToolResult, error) {
	encoded, err := protocol.Marshal(response)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("encode LSP result: %w", err)
	}

	var output bytes.Buffer
	if err := json.Indent(&output, encoded, "", "  "); err != nil {
		return lspSuccessResult(string(encoded)), nil
	}
	return lspSuccessResult(output.String()), nil
}

func lspErrorResult(name string, err error) agent.ToolResult {
	return agent.ToolResult{Output: boundLSPHead(fmt.Sprintf("%s: %v", name, err)), IsError: true}
}

func lspSuccessResult(output string) agent.ToolResult {
	return agent.ToolResult{Output: boundLSPHead(output)}
}

func boundLSPHead(text string) string {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	truncated := len(lines) > lspMaxOutputLines
	if truncated {
		lines = lines[:lspMaxOutputLines]
	}
	body := strings.Join(lines, "")
	if len(body) > lspMaxOutputBytes {
		body = lspPrefixUTF8(body, lspMaxOutputBytes)
		truncated = true
	}
	if !truncated {
		return text
	}
	const marker = "[output truncated]\n"
	body = lspPrefixUTF8(body, lspMaxOutputBytes-len(marker)-1)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + marker
}

func lspPrefixUTF8(text string, maximum int) string {
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
