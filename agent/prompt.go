package agent

import (
	"html"
	"path/filepath"
	"strings"
)

const baseSystemPrompt = `You are a coding agent. Use the available tools to inspect and modify code. Be concise and report results clearly.`

func buildSystemPrompt(definitions []ToolDefinition, workingDirectory, projectInstructions string, skills []Skill) string {
	var prompt strings.Builder
	prompt.WriteString(baseSystemPrompt)
	if len(definitions) > 0 {
		prompt.WriteString("\n\nIndependent tool calls may run concurrently; separate dependent calls.\n")
	}

	if projectInstructions != "" {
		instructionPath := "AGENTS.md"
		if workingDirectory != "" {
			instructionPath = filepath.Join(workingDirectory, instructionPath)
		}
		prompt.WriteString("\n<project_instructions path=\"")
		prompt.WriteString(html.EscapeString(filepath.ToSlash(instructionPath)))
		prompt.WriteString("\">\n")
		prompt.WriteString(strings.TrimSuffix(projectInstructions, "\n"))
		prompt.WriteString("\n</project_instructions>\n")
	}
	if skillPrompt := formatSkillsForPrompt(skills); skillPrompt != "" {
		prompt.WriteByte('\n')
		prompt.WriteString(skillPrompt)
		prompt.WriteByte('\n')
	}
	if workingDirectory != "" {
		prompt.WriteString("\nCurrent working directory: ")
		prompt.WriteString(filepath.ToSlash(workingDirectory))
		prompt.WriteByte('\n')
	}

	return strings.TrimSuffix(prompt.String(), "\n")
}
