package terminal

import (
	"context"
	"slices"
	"strings"
	"unicode"

	"github.com/eul-ai/eul/session"
	"github.com/eul-ai/eul/skill"
	"github.com/eul-ai/eul/terminal/keys"
)

const skillCommand = "/skill:"

type slashCommand struct {
	name        string
	usage       string
	description string
	duringRun   bool
	run         func(c *tuiController, ctx context.Context, arg string) (exit bool, err error)
}

var slashCommands = []slashCommand{
	{name: "/help", usage: "/help", description: "show this help", duringRun: true, run: (*tuiController).showHelp},
	{name: "/resume", usage: "/resume", description: "resume a saved session", run: (*tuiController).openResume},
	{name: "/new", usage: "/new", description: "start a new session", run: (*tuiController).startNewSession},
	{name: "/compact", usage: "/compact", description: "compact the conversation context", run: (*tuiController).compact},
	{name: "/fast", usage: "/fast", description: "toggle fast inference mode", duringRun: true, run: (*tuiController).toggleFastMode},
	{name: "/exit", usage: "/exit", description: "exit eul", run: (*tuiController).exit},
	{name: "/goal", usage: "/goal [objective|clear]", description: "show, set or clear the active goal", duringRun: true, run: (*tuiController).goal},
	{name: skillCommand, usage: "/skill:<name>", description: "load a skill", run: (*tuiController).loadSkill},
}

func (command slashCommand) acceptsArgument() bool {
	return command.usage != command.name
}

type commandCompletion struct {
	text               string
	description        string
	availableDuringRun bool
}

type commandPickerState struct {
	pickerList[commandCompletion]
	query      string
	tokenStart int
	tokenEnd   int
	active     bool
	dismissed  bool
}

func availableCommands(fastModeAvailable bool) []slashCommand {
	if fastModeAvailable {
		return slashCommands
	}
	return slices.DeleteFunc(slices.Clone(slashCommands), func(command slashCommand) bool {
		return command.name == "/fast"
	})
}

func commandCompletions(commands []slashCommand, skills []skill.Skill) []commandCompletion {
	completions := make([]commandCompletion, 0, len(commands)+len(skills))
	for _, command := range commands {
		if command.name != skillCommand {
			completions = append(completions, commandCompletion{
				text:               command.name,
				description:        command.description,
				availableDuringRun: command.duringRun,
			})
			continue
		}
		for _, skill := range skills {
			if !invokableSkillName(skill.Name) {
				continue
			}
			completions = append(completions, commandCompletion{
				text:        skillCommand + skill.Name,
				description: singleLine(skill.Description, 200),
			})
		}
	}
	return completions
}

func invokableSkillName(name string) bool {
	return name != "" && strings.IndexFunc(name, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) < 0
}

func commandHelpText(commands []slashCommand) string {
	const usageWidth = 24

	var help strings.Builder
	help.WriteString("Commands:")
	for _, command := range commands {
		help.WriteString("\n  ")
		help.WriteString(command.usage)
		help.WriteString(strings.Repeat(" ", max(1, usageWidth-len(command.usage))))
		help.WriteString(command.description)
	}
	return help.String()
}

func findCommand(commands []slashCommand, input string) (slashCommand, string, bool) {
	name, arg := input, ""
	if index := strings.IndexFunc(input, unicode.IsSpace); index >= 0 {
		name, arg = input[:index], strings.TrimSpace(input[index:])
	}
	for _, command := range commands {
		switch {
		case command.name == skillCommand && strings.HasPrefix(input, skillCommand):
			return command, input, true
		case command.name == name && (arg == "" || command.acceptsArgument()):
			return command, arg, true
		}
	}
	return slashCommand{}, "", false
}

func (c *tuiController) showHelp(context.Context, string) (bool, error) {
	c.model.appendEntry(session.EntryInfo, commandHelpText(c.model.commands))
	return false, nil
}

func (c *tuiController) openResume(context.Context, string) (bool, error) {
	summaries, err := c.model.session.Sessions()
	if err != nil {
		setInputError(c.model, err)
		return false, nil
	}
	c.model.openResumePicker(summaries)
	return false, nil
}

func (c *tuiController) startNewSession(context.Context, string) (bool, error) {
	c.cancelClipboardRequests()
	c.outcome = RunOutcome{Action: RunNewSession}
	return true, nil
}

func (c *tuiController) compact(context.Context, string) (bool, error) {
	return false, c.startCompaction()
}

func (c *tuiController) toggleFastMode(context.Context, string) (bool, error) {
	enabled := !c.model.session.Settings().FastMode
	c.model.session.SetFastMode(enabled)
	state := "off"
	if enabled {
		state = "on"
	}
	c.model.appendEntry(session.EntryInfo, "Fast mode "+state)
	return false, nil
}

func (c *tuiController) exit(context.Context, string) (bool, error) {
	c.cancelClipboardRequests()
	return true, nil
}

func (c *tuiController) goal(_ context.Context, objective string) (bool, error) {
	switch objective {
	case "":
		c.showGoal()
		return false, nil
	case "clear":
		c.clearGoal()
		return false, nil
	}

	if err := c.model.session.SetGoal(objective); err != nil {
		setInputError(c.model, err)
		return false, nil
	}
	if c.model.running {
		c.model.appendEntry(session.EntryInfo, "Goal set: "+objective)
		return false, nil
	}
	return false, c.startTurn(textContent(objective))
}

func (c *tuiController) showGoal() {
	goal, ok := c.model.session.Goal()
	switch {
	case !ok:
		c.model.appendEntry(session.EntryInfo, "No goal is set")
	case goal.Complete:
		c.model.appendEntry(session.EntryInfo, "Goal complete: "+goal.Objective)
	default:
		c.model.appendEntry(session.EntryInfo, "Goal: "+goal.Objective)
	}
}

func (c *tuiController) clearGoal() {
	_, hadGoal := c.model.session.Goal()
	c.model.session.ClearGoal()
	if hadGoal {
		c.model.appendEntry(session.EntryInfo, "Goal cleared")
		return
	}
	c.model.appendEntry(session.EntryInfo, "No goal is set")
}

func (c *tuiController) loadSkill(_ context.Context, prompt string) (bool, error) {
	return false, c.startTurn(textContent(prompt))
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
	start, end, query, ok := commandReference(m.inputReferenceRunes(), m.cursor)
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

	selected, _ := m.commandPicker.current()

	matches := make([]commandCompletion, 0, len(m.commandCompletions))
	for _, completion := range m.commandCompletions {
		if m.running && !completion.availableDuringRun {
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
	m.commandPicker.items = matches
	m.commandPicker.selected = max(0, slices.IndexFunc(matches, func(match commandCompletion) bool {
		return match.text == selected.text
	}))
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

func (m *tuiModel) applyCommandPickerSelection() error {
	picker := &m.commandPicker
	selected, ok := picker.current()
	if !ok {
		return nil
	}

	completion := selected.text
	removedBytes := len(editorText(m.input[picker.tokenStart:picker.tokenEnd]))
	if len(m.inputText())-removedBytes+len(completion) > maxInputBytes {
		return keys.ErrInputTooLong
	}

	m.leaveHistory()
	if !m.replaceItemRange(picker.tokenStart, picker.tokenEnd, completion) {
		return nil
	}
	m.dismissCommandPicker()
	return nil
}
