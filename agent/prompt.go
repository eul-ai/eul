package agent

import (
	"slices"
	"strings"
)

const baseSystemPrompt = `You are a coding agent. Use the available tools to inspect and modify code. Be concise and report results clearly.`

// BuildSystemPrompt constructs the deterministic MVP system prompt from the
// active tools. Tool definitions are sorted by name before rendering.
func BuildSystemPrompt(definitions []ToolDefinition) string {
	definitions = slices.Clone(definitions)
	slices.SortFunc(definitions, compareToolDefinitions)

	var prompt strings.Builder
	prompt.WriteString(baseSystemPrompt)
	prompt.WriteString("\n\nAvailable tools:\n")

	if len(definitions) == 0 {
		prompt.WriteString("(none)")
		return prompt.String()
	}

	guidelines := make([]string, 0)
	seenGuidelines := make(map[string]struct{})
	for _, definition := range definitions {
		summary := strings.TrimSpace(definition.PromptSummary)
		if summary == "" {
			summary = strings.TrimSpace(definition.Description)
		}

		prompt.WriteString("- ")
		prompt.WriteString(definition.Name)
		if summary != "" {
			prompt.WriteString(": ")
			prompt.WriteString(summary)
		}
		prompt.WriteByte('\n')

		for _, guideline := range definition.PromptGuidelines {
			guideline = strings.TrimSpace(guideline)
			if guideline == "" {
				continue
			}
			if _, exists := seenGuidelines[guideline]; exists {
				continue
			}
			seenGuidelines[guideline] = struct{}{}
			guidelines = append(guidelines, guideline)
		}
	}

	if len(guidelines) > 0 {
		prompt.WriteString("\nGuidelines:\n")
		for _, guideline := range guidelines {
			prompt.WriteString("- ")
			prompt.WriteString(guideline)
			prompt.WriteByte('\n')
		}
	}

	return strings.TrimSuffix(prompt.String(), "\n")
}

func compareToolDefinitions(a, b ToolDefinition) int {
	if order := strings.Compare(a.Name, b.Name); order != 0 {
		return order
	}
	if order := strings.Compare(promptSummary(a), promptSummary(b)); order != 0 {
		return order
	}
	return strings.Compare(strings.Join(a.PromptGuidelines, "\x00"), strings.Join(b.PromptGuidelines, "\x00"))
}

func promptSummary(definition ToolDefinition) string {
	summary := strings.TrimSpace(definition.PromptSummary)
	if summary != "" {
		return summary
	}
	return strings.TrimSpace(definition.Description)
}
