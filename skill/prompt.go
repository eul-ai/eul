package skill

import (
	"html"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

func PromptSection(skills []Skill) string {
	var entries strings.Builder
	for _, loaded := range skills {
		if loaded.DisableModelInvocation {
			continue
		}
		entries.WriteString("<skill><name>")
		entries.WriteString(html.EscapeString(loaded.Name))
		entries.WriteString("</name><description>")
		entries.WriteString(html.EscapeString(loaded.Description))
		entries.WriteString("</description><location>")
		entries.WriteString(html.EscapeString(filepath.ToSlash(loaded.FilePath)))
		entries.WriteString("</location></skill>")
	}
	if entries.Len() == 0 {
		return ""
	}

	return "The following skills provide specialized instructions for specific tasks.\n" +
		"Use the read tool to load a skill's file when the task matches its description.\n" +
		"Resolve skill-relative paths from the skill's SKILL.md directory.\n\n" +
		"<available_skills>" + entries.String() + "</available_skills>"
}

func Expand(text string, skills []Skill) (string, error) {
	remainder, ok := strings.CutPrefix(strings.TrimSpace(text), "/skill:")
	if !ok {
		return text, nil
	}
	name, arguments := remainder, ""
	if separator := strings.IndexFunc(remainder, unicode.IsSpace); separator >= 0 {
		name, arguments = remainder[:separator], strings.TrimSpace(remainder[separator:])
	}
	index := slices.IndexFunc(skills, func(loaded Skill) bool { return loaded.Name == name })
	if index < 0 {
		return text, nil
	}

	selected := skills[index]
	body, err := selected.Body()
	if err != nil {
		return "", err
	}

	var expanded strings.Builder
	expanded.WriteString(`<skill name="`)
	expanded.WriteString(html.EscapeString(selected.Name))
	expanded.WriteString(`" location="`)
	expanded.WriteString(html.EscapeString(filepath.ToSlash(selected.FilePath)))
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
