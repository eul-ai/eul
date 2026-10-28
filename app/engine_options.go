package app

import (
	"html"
	"path/filepath"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/subagent"
)

const (
	baseSystemPrompt       = `You are a coding agent. Use the available tools to inspect and modify code. Be concise and report results clearly.`
	childAgentInstructions = "This is a read-only research task. Inspect and report findings, but do not modify files. The main agent will make any edits."
)

func engineOptions(config resolvedConfig, model string, settings *agent.Settings, checkpointing bool, inbox agent.Inbox) agent.Options {
	return agent.Options{
		Model:         model,
		Settings:      settings,
		Instructions:  systemPrompt(config),
		Checkpointing: checkpointing,
		Inbox:         inbox,
	}
}

func childEngineOptions(config resolvedConfig, profile subagent.Profile, thinkingLevel agent.ThinkingLevel, fastMode bool) agent.Options {
	options := engineOptions(config, config.models.forProfile(profile), agent.NewSettings(thinkingLevel, fastMode), false, nil)
	options.Instructions += "\n\n" + childAgentInstructions
	return options
}

func systemPrompt(config resolvedConfig) string {
	var prompt strings.Builder
	prompt.WriteString(baseSystemPrompt)
	prompt.WriteString("\n\nIndependent tool calls may run concurrently; separate dependent calls.\n")
	if config.projectInstructions != "" {
		prompt.WriteString("\n<project_instructions path=\"")
		prompt.WriteString(html.EscapeString(filepath.ToSlash(filepath.Join(config.cwd, "AGENTS.md"))))
		prompt.WriteString("\">\n")
		prompt.WriteString(strings.TrimSuffix(config.projectInstructions, "\n"))
		prompt.WriteString("\n</project_instructions>\n")
	}
	if section := skill.PromptSection(config.skills); section != "" {
		prompt.WriteString("\n" + section + "\n")
	}
	prompt.WriteString("\nCurrent working directory: ")
	prompt.WriteString(filepath.ToSlash(config.cwd))
	return prompt.String()
}
