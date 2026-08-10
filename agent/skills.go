package agent

import (
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type Skill struct {
	Name                   string
	Description            string
	FilePath               string
	DisableModelInvocation bool
}

type skillFrontmatter struct {
	name                   string
	description            string
	disableModelInvocation bool
}

func LoadSkills(directories ...string) ([]Skill, []string) {
	var loaded []Skill
	var warnings []string
	visitedDirectories := make(map[string]struct{})

	for _, directory := range directories {
		directorySkills, directoryWarnings := loadSkillsFromDirectory(directory, visitedDirectories)
		loaded = append(loaded, directorySkills...)
		warnings = append(warnings, directoryWarnings...)
	}

	skills := make([]Skill, 0, len(loaded))
	files := make(map[string]struct{})
	names := make(map[string]struct{})
	for _, skill := range loaded {
		canonicalPath := canonicalSkillPath(skill.FilePath)
		if _, exists := files[canonicalPath]; exists {
			continue
		}
		files[canonicalPath] = struct{}{}

		if _, exists := names[skill.Name]; exists {
			continue
		}
		names[skill.Name] = struct{}{}
		skills = append(skills, skill)
	}

	return skills, warnings
}

func loadSkillsFromDirectory(directory string, visited map[string]struct{}) ([]Skill, []string) {
	absolutePath, err := filepath.Abs(directory)
	if err != nil {
		return nil, []string{skillWarning(directory, err)}
	}

	info, err := os.Stat(absolutePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{skillWarning(absolutePath, err)}
	}
	if !info.IsDir() {
		return nil, []string{skillWarning(absolutePath, errors.New("skill path is not a directory"))}
	}

	canonicalDirectory := canonicalSkillPath(absolutePath)
	if _, exists := visited[canonicalDirectory]; exists {
		return nil, nil
	}
	visited[canonicalDirectory] = struct{}{}

	entries, err := os.ReadDir(absolutePath)
	if err != nil {
		return nil, []string{skillWarning(absolutePath, err)}
	}

	var warnings []string
	for _, entry := range entries {
		if entry.Name() != "SKILL.md" {
			continue
		}

		skillPath := filepath.Join(absolutePath, entry.Name())
		skillInfo, err := os.Stat(skillPath)
		if err != nil {
			return nil, []string{skillWarning(skillPath, err)}
		}
		if !skillInfo.Mode().IsRegular() {
			warnings = append(warnings, skillWarning(skillPath, errors.New("skill file is not a regular file")))
			break
		}

		skill, err := loadSkillFile(skillPath)
		if err != nil {
			return nil, []string{skillWarning(skillPath, err)}
		}
		return []Skill{skill}, nil
	}

	var skills []Skill
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}

		path := filepath.Join(absolutePath, name)
		entryInfo, err := os.Stat(path)
		if err != nil {
			warnings = append(warnings, skillWarning(path, err))
			continue
		}
		if !entryInfo.IsDir() {
			continue
		}

		directorySkills, directoryWarnings := loadSkillsFromDirectory(path, visited)
		skills = append(skills, directorySkills...)
		warnings = append(warnings, directoryWarnings...)
	}

	return skills, warnings
}

func loadSkillFile(path string) (Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	frontmatter, _, err := parseSkillFrontmatter(string(content))
	if err != nil {
		return Skill{}, err
	}
	if strings.TrimSpace(frontmatter.description) == "" {
		return Skill{}, errors.New("description is empty")
	}

	name := frontmatter.name
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}

	return Skill{
		Name:                   name,
		Description:            frontmatter.description,
		FilePath:               path,
		DisableModelInvocation: frontmatter.disableModelInvocation,
	}, nil
}

func skillWarning(path string, err error) string {
	return fmt.Sprintf("Skipped skill %s: %v", filepath.ToSlash(path), err)
}

func parseSkillFrontmatter(content string) (skillFrontmatter, string, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return skillFrontmatter{}, normalized, errors.New("missing opening frontmatter delimiter")
	}

	closingLine := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closingLine = index
			break
		}
	}
	if closingLine == -1 {
		return skillFrontmatter{}, normalized, errors.New("missing closing frontmatter delimiter")
	}

	var frontmatter skillFrontmatter
	seen := make(map[string]struct{})
	for index, line := range lines[1:closingLine] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || line[0] == ' ' || line[0] == '\t' {
			continue
		}

		key, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			return skillFrontmatter{}, normalized, fmt.Errorf("frontmatter line %d must contain ':'", index+2)
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" && key != "disable-model-invocation" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return skillFrontmatter{}, normalized, fmt.Errorf("frontmatter field %q is duplicated", key)
		}
		seen[key] = struct{}{}

		rawValue = strings.TrimSpace(rawValue)
		switch key {
		case "name":
			value, err := parseSkillString(rawValue)
			if err != nil {
				return skillFrontmatter{}, normalized, fmt.Errorf("name: %w", err)
			}
			frontmatter.name = value
		case "description":
			value, err := parseSkillString(rawValue)
			if err != nil {
				return skillFrontmatter{}, normalized, fmt.Errorf("description: %w", err)
			}
			frontmatter.description = value
		case "disable-model-invocation":
			switch rawValue {
			case "true":
				frontmatter.disableModelInvocation = true
			case "false":
				frontmatter.disableModelInvocation = false
			default:
				return skillFrontmatter{}, normalized, errors.New("disable-model-invocation must be true or false")
			}
		}
	}

	return frontmatter, strings.Join(lines[closingLine+1:], "\n"), nil
}

func parseSkillString(value string) (string, error) {
	switch value {
	case "|", "|-", "|+", ">", ">-", ">+":
		return "", errors.New("block scalars are not supported")
	case "":
		return "", nil
	}

	switch value[0] {
	case '\'':
		return parseSingleQuotedSkillString(value)
	case '"':
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("unterminated double-quoted string")
		}
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted string: %w", err)
		}
		return unquoted, nil
	default:
		if strings.ContainsRune("[{&*!", rune(value[0])) {
			return "", errors.New("non-scalar values are not supported")
		}
		return value, nil
	}
}

func parseSingleQuotedSkillString(value string) (string, error) {
	if len(value) < 2 || value[len(value)-1] != '\'' {
		return "", errors.New("unterminated single-quoted string")
	}

	inner := value[1 : len(value)-1]
	var parsed strings.Builder
	for index := 0; index < len(inner); index++ {
		if inner[index] != '\'' {
			parsed.WriteByte(inner[index])
			continue
		}
		if index+1 >= len(inner) || inner[index+1] != '\'' {
			return "", errors.New("single quotes inside a quoted string must be doubled")
		}
		parsed.WriteByte('\'')
		index++
	}
	return parsed.String(), nil
}

func canonicalSkillPath(path string) string {
	canonical, err := filepath.EvalSymlinks(path)
	if err == nil {
		return canonical
	}
	return filepath.Clean(path)
}

func formatSkillsForPrompt(skills []Skill) string {
	var visible []Skill
	for _, skill := range skills {
		if !skill.DisableModelInvocation {
			visible = append(visible, skill)
		}
	}
	if len(visible) == 0 {
		return ""
	}

	var prompt strings.Builder
	prompt.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	prompt.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	prompt.WriteString("When a skill file references a relative path, resolve it against the skill directory containing SKILL.md and use that absolute path in tool commands.\n\n")
	prompt.WriteString("<available_skills>")
	for _, skill := range visible {
		prompt.WriteString("<skill>")
		prompt.WriteString("<name>")
		prompt.WriteString(html.EscapeString(skill.Name))
		prompt.WriteString("</name>")
		prompt.WriteString("<description>")
		prompt.WriteString(html.EscapeString(skill.Description))
		prompt.WriteString("</description>")
		prompt.WriteString("<location>")
		prompt.WriteString(html.EscapeString(filepath.ToSlash(skill.FilePath)))
		prompt.WriteString("</location>")
		prompt.WriteString("</skill>")
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}

func expandSkillCommand(text string, skills map[string]Skill) (string, error) {
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

	skill, exists := skills[name]
	if !exists {
		return text, nil
	}

	content, err := os.ReadFile(skill.FilePath)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	_, body, err := parseSkillFrontmatter(string(content))
	if err != nil {
		return "", fmt.Errorf("parse skill %q: %w", name, err)
	}

	var expanded strings.Builder
	expanded.WriteString(`<skill name="`)
	expanded.WriteString(html.EscapeString(skill.Name))
	expanded.WriteString(`" location="`)
	expanded.WriteString(html.EscapeString(filepath.ToSlash(skill.FilePath)))
	expanded.WriteString("\">\nReferences are relative to ")
	expanded.WriteString(html.EscapeString(filepath.ToSlash(filepath.Dir(skill.FilePath))))
	expanded.WriteString(".\n\n")
	expanded.WriteString(strings.TrimSpace(body))
	expanded.WriteString("\n</skill>")
	if arguments != "" {
		expanded.WriteString("\n\n")
		expanded.WriteString(arguments)
	}
	return expanded.String(), nil
}
