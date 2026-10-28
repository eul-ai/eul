package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type Skill struct {
	Name                   string
	Description            string
	FilePath               string
	DisableModelInvocation bool
}

type loader struct {
	visited  map[string]struct{}
	names    map[string]struct{}
	skills   []Skill
	warnings []string
}

func Load(directories ...string) ([]Skill, []string) {
	l := loader{visited: make(map[string]struct{}), names: make(map[string]struct{})}
	for _, directory := range directories {
		l.loadDirectory(directory)
	}
	return l.skills, l.warnings
}

func (l *loader) warn(path string, err error) {
	l.warnings = append(l.warnings, fmt.Sprintf("Skipped skill %s: %v", filepath.ToSlash(path), err))
}

func (l *loader) loadDirectory(directory string) {
	path, err := filepath.Abs(directory)
	if err != nil {
		l.warn(directory, err)
		return
	}
	canonical, err := filepath.EvalSymlinks(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		l.warn(path, err)
		return
	}
	if _, visited := l.visited[canonical]; visited {
		return
	}
	l.visited[canonical] = struct{}{}

	skillPath := filepath.Join(path, "SKILL.md")
	if info, err := os.Stat(skillPath); err == nil && info.Mode().IsRegular() {
		l.loadFile(skillPath)
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		l.warn(path, err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}

		child := filepath.Join(path, name)
		info, err := os.Stat(child)
		if err != nil {
			l.warn(child, err)
			continue
		}
		if info.IsDir() {
			l.loadDirectory(child)
		}
	}
}

func (l *loader) loadFile(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		l.warn(path, err)
		return
	}
	loaded, _, err := parseSkill(string(content))
	if err != nil {
		l.warn(path, err)
		return
	}
	if strings.TrimSpace(loaded.Description) == "" {
		l.warn(path, errors.New("description is empty"))
		return
	}

	if loaded.Name == "" {
		loaded.Name = filepath.Base(filepath.Dir(path))
	}
	if _, exists := l.names[loaded.Name]; exists {
		return
	}
	l.names[loaded.Name] = struct{}{}
	loaded.FilePath = path
	l.skills = append(l.skills, loaded)
}

func parseSkill(content string) (Skill, string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), "\n")
	if lines[0] != "---" {
		return Skill{}, "", errors.New("missing opening frontmatter delimiter")
	}
	closing := slices.Index(lines[1:], "---") + 1
	if closing == 0 {
		return Skill{}, "", errors.New("missing closing frontmatter delimiter")
	}

	var parsed Skill
	for _, line := range lines[1:closing] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = unquote(strings.TrimSpace(value))
		switch strings.TrimRight(key, " \t") {
		case "name":
			parsed.Name = value
		case "description":
			parsed.Description = value
		case "disable-model-invocation":
			parsed.DisableModelInvocation = value == "true"
		}
	}
	return parsed, strings.Join(lines[closing+1:], "\n"), nil
}

func unquote(value string) string {
	switch {
	case strings.HasPrefix(value, `"`):
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func (s Skill) Body() (string, error) {
	content, err := os.ReadFile(s.FilePath)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", s.Name, err)
	}
	_, body, err := parseSkill(string(content))
	if err != nil {
		return "", fmt.Errorf("parse skill %q: %w", s.Name, err)
	}
	return body, nil
}
