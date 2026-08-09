package terminal

import (
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type tuiActionKind uint8

const (
	tuiActionNone tuiActionKind = iota
	tuiActionHelp
	tuiActionCancel
	tuiActionReset
	tuiActionCompact
	tuiActionExit
	tuiActionSubmit
	tuiActionSteer
	tuiActionShowGoal
	tuiActionSetGoal
	tuiActionClearGoal
	tuiActionDequeue
	tuiActionSetThinking
	tuiActionCopy
	tuiActionRedraw
)

type tuiAction struct {
	kind          tuiActionKind
	prompt        string
	text          string
	thinkingLevel agent.ThinkingLevel
}

func reduceKeyWithFrame(model *tuiModel, key keyEvent, frame terminalFrame) (tuiAction, error) {
	if key.code == keyMouse {
		return reduceMouse(model, key.mouse, frame), nil
	}
	if reduceCommandPickerKey(model, key) {
		return tuiAction{}, nil
	}
	if reduceFilePickerKey(model, key) {
		return tuiAction{}, nil
	}

	switch key.code {
	case keyFailure:
		if key.fatal {
			return tuiAction{}, fmt.Errorf("terminal: read input: %w", key.err)
		}
		if !model.running {
			setInputError(model, key.err)
		}
		return tuiAction{}, nil
	case keyEOF:
		return tuiAction{kind: tuiActionExit}, nil
	case keyEscape:
		if model.running {
			return reduceInterrupt(model)
		}
	case keyCtrlC:
		if model.running {
			return reduceInterrupt(model)
		}
		if len(model.input) > 0 {
			model.clearInput()
			return tuiAction{}, nil
		}
		return tuiAction{}, ErrInterrupted
	case keyCtrlL:
		return tuiAction{kind: tuiActionRedraw}, nil
	case keyPageUp:
		scrollConversation(model, -1, frame)
		return tuiAction{}, nil
	case keyPageDown:
		scrollConversation(model, 1, frame)
		return tuiAction{}, nil
	case keyAltUp:
		return tuiAction{kind: tuiActionDequeue}, nil
	}

	switch key.code {
	case keyText:
		if err := model.insertInput(key.text); err != nil {
			setInputError(model, err)
		}
	case keyNewline:
		if err := model.insertNewline(); err != nil {
			setInputError(model, err)
		}
	case keyShiftTab:
		if model.running {
			return tuiAction{}, nil
		}
		level, err := model.nextThinkingLevel()
		if err != nil {
			setInputError(model, err)
			return tuiAction{}, nil
		}
		return tuiAction{kind: tuiActionSetThinking, thinkingLevel: level}, nil
	case keyLeft:
		model.moveLeft()
	case keyRight:
		model.moveRight()
	case keyHome:
		model.cursor = 0
		model.refreshInputPickers(false)
	case keyEnd:
		model.cursor = len(model.input)
		model.refreshInputPickers(false)
	case keyBackspace:
		model.backspace()
	case keyDelete:
		model.delete()
	case keyUp:
		model.historyUp()
	case keyDown:
		model.historyDown()
	case keyCtrlD:
		if !model.running && len(model.input) == 0 {
			return tuiAction{kind: tuiActionExit}, nil
		}
	case keyEnter:
		if model.running {
			return reduceSteeringPrompt(model), nil
		}
		return reducePrompt(model), nil
	}
	return tuiAction{}, nil
}

func reduceCommandPickerKey(model *tuiModel, key keyEvent) bool {
	if !model.commandPickerVisible() {
		return false
	}

	switch key.code {
	case keyUp:
		model.moveCommandPickerSelection(-1)
	case keyDown:
		model.moveCommandPickerSelection(1)
	case keyTab:
		if err := model.applyCommandPickerSelection(); err != nil {
			setInputError(model, err)
		}
	case keyEnter:
		selected := model.commandPicker.matches[model.commandPicker.selected]
		if selected.text == model.commandPicker.query {
			return false
		}
		if err := model.applyCommandPickerSelection(); err != nil {
			setInputError(model, err)
			return true
		}
		return false
	case keyEscape:
		model.dismissCommandPicker()
	default:
		return false
	}
	return true
}

func reduceFilePickerKey(model *tuiModel, key keyEvent) bool {
	if key.code == keyEscape && model.filePicker.active {
		model.dismissFilePicker()
		return true
	}
	if !model.filePickerVisible() {
		return false
	}
	if !model.filePicker.loading && len(model.filePicker.matches) == 0 {
		return false
	}

	switch key.code {
	case keyUp:
		model.moveFilePickerSelection(-1)
	case keyDown:
		model.moveFilePickerSelection(1)
	case keyEnter, keyTab:
		if !model.filePicker.loading {
			if err := model.applyFilePickerSelection(); err != nil {
				setInputError(model, err)
			}
		}
	case keyEscape:
		model.dismissFilePicker()
	default:
		return false
	}
	return true
}

func reduceInterrupt(model *tuiModel) (tuiAction, error) {
	if !model.running {
		return tuiAction{}, ErrInterrupted
	}
	if model.interrupted {
		return tuiAction{}, nil
	}

	model.interrupted = true
	model.activity = activity{kind: activityCanceling}
	return tuiAction{kind: tuiActionCancel}, nil
}

func reduceSteeringPrompt(model *tuiModel) tuiAction {
	prompt := string(model.input)
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return tuiAction{}
	}
	if strings.HasPrefix(trimmed, "/") {
		action, command, ok := matchSlashCommand(prompt, trimmed)
		if ok && command.availableDuringRun {
			model.takePrompt()
			return action
		}
		setInputError(model, fmt.Errorf("commands cannot be queued while the agent is running"))
		return tuiAction{}
	}

	prompt, _ = model.takePrompt()
	return tuiAction{kind: tuiActionSteer, prompt: prompt}
}

func reducePrompt(model *tuiModel) tuiAction {
	prompt, ok := model.takePrompt()
	if !ok {
		return tuiAction{}
	}

	trimmed := strings.TrimSpace(prompt)
	if action, _, matched := matchSlashCommand(prompt, trimmed); matched {
		if action.kind == tuiActionSubmit {
			model.beginTurn(prompt)
		}
		return action
	}
	if strings.HasPrefix(trimmed, "/") {
		model.appendBlock(blockError, "Unknown command "+diagnostic(trimmed, 120))
		model.activity = activity{kind: activityError, detail: "unknown command"}
		return tuiAction{}
	}

	model.beginTurn(prompt)
	return tuiAction{kind: tuiActionSubmit, prompt: prompt}
}

func setInputError(model *tuiModel, err error) {
	detail := diagnostic(err.Error(), 200)
	model.activity = activity{kind: activityError, detail: detail}
}
