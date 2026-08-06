package agent

import "strings"

const baseSystemPrompt = `You are a coding agent. Use the available tools to inspect and modify code. Be concise and report results clearly.`

func buildSystemPrompt(definitions []ToolDefinition, projectInstructions string) string {
	var prompt strings.Builder
	prompt.WriteString(baseSystemPrompt)
	prompt.WriteString("\n\nAvailable tools:\n")

	if len(definitions) == 0 {
		prompt.WriteString("(none)")
	}
	for _, definition := range definitions {
		prompt.WriteString("- ")
		prompt.WriteString(definition.Name)
		if description := strings.TrimSpace(definition.Description); description != "" {
			prompt.WriteString(": ")
			prompt.WriteString(description)
		}
		prompt.WriteByte('\n')
	}

	result := strings.TrimSuffix(prompt.String(), "\n")
	if projectInstructions == "" {
		return result
	}

	return result + "\n\n" + strings.TrimSuffix(projectInstructions, "\n")
}
