package terminal

import (
	"strings"
	"unicode"

	"github.com/eul-ai/eul/agent"
)

const commandPickerMaxVisible = 5

type slashCommandDefinition struct {
	text        string
	usage       string
	description string
}

var slashCommandDefinitions = []slashCommandDefinition{
	{text: "/help", usage: "/help", description: "show this help"},
	{text: "/clear", usage: "/clear", description: "discard conversation and goal state"},
	{text: "/compact", usage: "/compact", description: "compact the conversation context"},
	{text: "/exit", usage: "/exit", description: "exit eul"},
	{text: "/goal", usage: "/goal [objective]", description: "show or set the active goal"},
	{text: "/goal clear", usage: "/goal clear", description: "clear the active goal"},
	{usage: "/skill:<name>", description: "load a skill"},
}

type commandCompletion struct {
	text        string
	description string
}

type commandPickerState struct {
	matches    []commandCompletion
	query      string
	tokenStart int
	tokenEnd   int
	selected   int
	active     bool
	dismissed  bool
}

func commandCompletions(skills []agent.Skill) []commandCompletion {
	completions := make([]commandCompletion, 0, len(slashCommandDefinitions)+len(skills))
	for _, command := range slashCommandDefinitions {
		if command.text == "" {
			continue
		}
		completions = append(completions, commandCompletion{text: command.text, description: command.description})
	}
	for _, skill := range skills {
		if !invokableSkillName(skill.Name) {
			continue
		}
		completions = append(completions, commandCompletion{
			text:        "/skill:" + skill.Name,
			description: singleLine(skill.Description, 200),
		})
	}
	return completions
}

func invokableSkillName(name string) bool {
	return name != "" && strings.IndexFunc(name, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) < 0
}

func commandHelpText() string {
	const usageWidth = 18

	var help strings.Builder
	help.WriteString("Commands:")
	for _, command := range slashCommandDefinitions {
		help.WriteString("\n  ")
		help.WriteString(command.usage)
		padding := usageWidth - len(command.usage)
		if padding < 1 {
			padding = 1
		}
		help.WriteString(strings.Repeat(" ", padding))
		help.WriteString(command.description)
	}
	return help.String()
}

func commandReference(input []rune, cursor int) (int, int, string, bool) {
	if cursor < 0 || cursor > len(input) {
		return 0, 0, "", false
	}

	start := 0
	for start < len(input) && unicode.IsSpace(input[start]) {
		start++
	}
	if start >= len(input) || start >= cursor || input[start] != '/' {
		return 0, 0, "", false
	}

	end := cursor
	for end < len(input) && !unicode.IsSpace(input[end]) {
		end++
	}
	return start, end, string(input[start:cursor]), true
}

func (m *tuiModel) refreshCommandPicker(reopen bool) {
	start, end, query, ok := commandReference(m.input, m.cursor)
	if !ok {
		m.clearCommandPicker()
		return
	}
	if reopen {
		m.commandPicker.dismissed = false
	}
	if m.commandPicker.dismissed || !m.commandPicker.active && !reopen {
		return
	}

	selectedText := ""
	if m.commandPicker.selected >= 0 && m.commandPicker.selected < len(m.commandPicker.matches) {
		selectedText = m.commandPicker.matches[m.commandPicker.selected].text
	}

	matches := make([]commandCompletion, 0, len(m.commandCompletions))
	for _, completion := range m.commandCompletions {
		if m.running && completion.text != "/goal clear" {
			continue
		}
		if strings.HasPrefix(completion.text, query) {
			matches = append(matches, completion)
		}
	}
	if len(matches) == 0 {
		m.clearCommandPicker()
		return
	}

	m.commandPicker.active = true
	m.commandPicker.query = query
	m.commandPicker.tokenStart = start
	m.commandPicker.tokenEnd = end
	m.commandPicker.matches = matches
	m.commandPicker.selected = 0
	for index, match := range matches {
		if match.text == selectedText {
			m.commandPicker.selected = index
			break
		}
	}
}

func (m *tuiModel) refreshCommandPickerAvailability() {
	if m.commandPicker.dismissed {
		return
	}
	m.commandPicker.active = true
	m.refreshCommandPicker(false)
}

func (m *tuiModel) clearCommandPicker() {
	m.commandPicker = commandPickerState{}
}

func (m *tuiModel) dismissCommandPicker() {
	m.commandPicker = commandPickerState{dismissed: true}
}

func (m *tuiModel) commandPickerVisible() bool {
	return maximumPickerHeight(m.height) > 0 && m.commandPicker.active
}

func (m *tuiModel) commandPickerHeight() int {
	if !m.commandPickerVisible() {
		return 0
	}
	return min(commandPickerMaxVisible, len(m.commandPicker.matches))
}

func (m *tuiModel) moveCommandPickerSelection(direction int) {
	count := len(m.commandPicker.matches)
	if count == 0 {
		return
	}
	m.commandPicker.selected = (m.commandPicker.selected + direction + count) % count
}

func (m *tuiModel) applyCommandPickerSelection() error {
	picker := &m.commandPicker
	if picker.selected < 0 || picker.selected >= len(picker.matches) {
		return nil
	}

	completion := picker.matches[picker.selected].text
	before := string(m.input[:picker.tokenStart])
	after := string(m.input[picker.tokenEnd:])
	if len(before)+len(completion)+len(after) > maxInputBytes {
		return errInputTooLong
	}

	m.leaveHistory()
	m.input = []rune(before + completion + after)
	m.cursor = len([]rune(before + completion))
	m.dismissCommandPicker()
	return nil
}

func (m *tuiModel) visibleCommandPickerMatches() []commandCompletion {
	matches := m.commandPicker.matches
	if len(matches) <= commandPickerMaxVisible {
		return matches
	}
	start := m.commandPicker.selected - commandPickerMaxVisible/2
	start = max(0, min(start, len(matches)-commandPickerMaxVisible))
	return matches[start : start+commandPickerMaxVisible]
}
