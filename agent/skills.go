package agent

import (
	"html"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/skill"
)

func skillName(loaded skill.Skill) string {
	return html.EscapeString(loaded.Name)
}

func skillLocation(loaded skill.Skill) string {
	return html.EscapeString(filepath.ToSlash(loaded.FilePath))
}

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
		prompt.WriteString(skillName(loaded))
		prompt.WriteString("</name>")
		prompt.WriteString("<description>")
		prompt.WriteString(html.EscapeString(loaded.Description))
		prompt.WriteString("</description>")
		prompt.WriteString("<location>")
		prompt.WriteString(skillLocation(loaded))
		prompt.WriteString("</location>")
		prompt.WriteString("</skill>")
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}

func expandSkillCommand(text string, skills []skill.Skill) (string, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/skill:") {
		return text, nil
	}

	remainder := strings.TrimPrefix(trimmed, "/skill:")
	separator := strings.IndexFunc(remainder, unicode.IsSpace)
	name := remainder
	arguments := ""
	if separator >= 0 {
		name = remainder[:separator]
		arguments = strings.TrimSpace(remainder[separator:])
	}

	var selected *skill.Skill
	for index := range skills {
		if skills[index].Name == name {
			selected = &skills[index]
			break
		}
	}
	if selected == nil {
		return text, nil
	}

	body, err := skill.ReadBody(*selected)
	if err != nil {
		return "", err
	}

	var expanded strings.Builder
	expanded.WriteString(`<skill name="`)
	expanded.WriteString(skillName(*selected))
	expanded.WriteString(`" location="`)
	expanded.WriteString(skillLocation(*selected))
	expanded.WriteString("\">\nReferences are relative to ")
	expanded.WriteString(html.EscapeString(filepath.ToSlash(filepath.Dir(selected.FilePath))))
	expanded.WriteString(".\n\n")
	expanded.WriteString(strings.TrimSpace(body))
	expanded.WriteString("\n</skill>")
	if arguments != "" {
		expanded.WriteString("\n\n")
		expanded.WriteString(arguments)
	}
	return expanded.String(), nil
}
