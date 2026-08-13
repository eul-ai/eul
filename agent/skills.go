package agent

import (
	"html"
	"path/filepath"
	"strings"

	"github.com/eul-ai/eul/skill"
)

func formatSkillsForPrompt(skills []skill.Skill) string {
	var visible []skill.Skill
	for _, loaded := range skills {
		if !loaded.DisableModelInvocation {
			visible = append(visible, loaded)
		}
	}
	if len(visible) == 0 {
		return ""
	}

	var prompt strings.Builder
	prompt.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	prompt.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	prompt.WriteString("Resolve skill-relative paths from the skill's SKILL.md directory.\n\n")
	prompt.WriteString("<available_skills>")
	for _, loaded := range visible {
		prompt.WriteString("<skill>")
		prompt.WriteString("<name>")
		prompt.WriteString(html.EscapeString(loaded.Name))
		prompt.WriteString("</name>")
		prompt.WriteString("<description>")
		prompt.WriteString(html.EscapeString(loaded.Description))
		prompt.WriteString("</description>")
		prompt.WriteString("<location>")
		prompt.WriteString(html.EscapeString(filepath.ToSlash(loaded.FilePath)))
		prompt.WriteString("</location>")
		prompt.WriteString("</skill>")
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}
