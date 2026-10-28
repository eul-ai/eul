package terminal

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/filesearch"
	"github.com/eul-ai/eul/terminal/keys"
)

type tuiActionKind uint8

const (
	tuiActionNone tuiActionKind = iota
	tuiActionCommand
	tuiActionResume
	tuiActionCancel
	tuiActionExit
	tuiActionSubmit
	tuiActionSteer
	tuiActionDequeue
	tuiActionSetThinking
	tuiActionAttachImage
	tuiActionResolvePermission
	tuiActionCopy
	tuiActionRedraw
)

type tuiAction struct {
	kind               tuiActionKind
	command            slashCommand
	text               string
	content            []agent.ContentPart
	thinkingLevel      agent.ThinkingLevel
	permissionDecision PermissionDecision
}

func handleKeyInput(model *tuiModel, key keys.Event, frame terminalFrame) (tuiAction, error) {
	if model.permission.active() {
		return reducePermissionKey(model, key), nil
	}
	if key.Code == keys.MouseInput {
		return reduceMouse(model, key.Mouse, frame), nil
	}
	if action, handled := reduceResumePickerKey(model, key); handled {
		return action, nil
	}
	if reduceCommandPickerKey(model, key) {
		return tuiAction{}, nil
	}
	if reduceFilePickerKey(model, key) {
		return tuiAction{}, nil
	}

	switch key.Code {
	case keys.Failure:
		if key.Fatal {
			return tuiAction{}, fmt.Errorf("terminal: read input: %w", key.Err)
		}
		if !model.running {
			setInputError(model, key.Err)
		}
		return tuiAction{}, nil
	case keys.EOF:
		return tuiAction{kind: tuiActionExit}, nil
	case keys.Escape:
		if model.running {
			return reduceInterrupt(model)
		}
	case keys.CtrlC:
		if model.running {
			return reduceInterrupt(model)
		}
		if len(model.input) > 0 {
			model.clearInput()
			return tuiAction{}, nil
		}
		return tuiAction{}, ErrInterrupted
	case keys.CtrlL:
		return tuiAction{kind: tuiActionRedraw}, nil
	case keys.CtrlV:
		return tuiAction{kind: tuiActionAttachImage}, nil
	case keys.PageUp:
		scrollConversation(model, -1, frame)
		return tuiAction{}, nil
	case keys.PageDown:
		scrollConversation(model, 1, frame)
		return tuiAction{}, nil
	case keys.AltDown:
		scrollConversationToBottom(model, frame)
		return tuiAction{}, nil
	case keys.AltUp:
		return tuiAction{kind: tuiActionDequeue}, nil
	}

	switch key.Code {
	case keys.Text:
		if err := model.insertInput(key.Text); err != nil {
			setInputError(model, err)
		}
	case keys.Newline:
		if err := model.insertNewline(); err != nil {
			setInputError(model, err)
		}
	case keys.ShiftTab:
		level, err := model.nextThinkingLevel()
		if err != nil {
			setInputError(model, err)
			return tuiAction{}, nil
		}
		return tuiAction{kind: tuiActionSetThinking, thinkingLevel: level}, nil
	case keys.Left:
		model.moveLeft()
	case keys.Right:
		model.moveRight()
	case keys.Home:
		model.moveHome()
	case keys.End:
		model.moveEnd()
	case keys.Backspace:
		model.backspace()
	case keys.Delete:
		model.delete()
	case keys.Up:
		if !model.moveUp() {
			model.historyUp()
		}
	case keys.Down:
		if !model.moveDown() {
			model.historyDown()
		}
	case keys.CtrlD:
		if !model.running && len(model.input) == 0 {
			return tuiAction{kind: tuiActionExit}, nil
		}
	case keys.Enter:
		return reduceSubmission(model), nil
	}
	return tuiAction{}, nil
}

func reducePermissionKey(model *tuiModel, key keys.Event) tuiAction {
	switch key.Code {
	case keys.Text:
		switch strings.ToLower(strings.TrimSpace(key.Text)) {
		case "a":
			return permissionAction(PermissionAllowOnce)
		case "d":
			return permissionAction(PermissionDenyOnce)
		case "s":
			return permissionAction(PermissionAllowSession)
		}
	case keys.Left:
		if model.permission.selected > PermissionDenyOnce {
			model.permission.selected--
		}
	case keys.Right:
		if model.permission.selected < PermissionAllowSession {
			model.permission.selected++
		}
	case keys.Tab:
		model.permission.selected = (model.permission.selected + 1) % permissionDecisionCount
	case keys.ShiftTab:
		model.permission.selected = (model.permission.selected + permissionDecisionCount - 1) % permissionDecisionCount
	case keys.Up:
		scrollPermission(model, -1)
	case keys.Down:
		scrollPermission(model, 1)
	case keys.PageUp:
		scrollPermission(model, -permissionDetailCapacityForModel(model))
	case keys.PageDown:
		scrollPermission(model, permissionDetailCapacityForModel(model))
	case keys.Enter:
		return permissionAction(model.permission.selected)
	case keys.Escape:
		return permissionAction(PermissionDenyOnce)
	case keys.CtrlC:
		if model.running {
			return tuiAction{kind: tuiActionCancel}
		}
		return permissionAction(PermissionDenyOnce)
	case keys.EOF, keys.CtrlD:
		return tuiAction{kind: tuiActionExit}
	}
	return tuiAction{}
}

func permissionAction(decision PermissionDecision) tuiAction {
	return tuiAction{kind: tuiActionResolvePermission, permissionDecision: decision}
}

func scrollPermission(model *tuiModel, delta int) {
	lines := wrappedPermissionDetail(model.permission, model.width)
	maximum := max(0, len(lines)-permissionDetailCapacityForModel(model))
	model.permission.scroll = max(0, min(maximum, model.permission.scroll+delta))
}

func reduceResumePickerKey(model *tuiModel, key keys.Event) (tuiAction, bool) {
	if !model.resumePickerVisible() {
		return tuiAction{}, false
	}

	switch key.Code {
	case keys.Up:
		model.moveResumePickerSelection(-1)
	case keys.Down:
		model.moveResumePickerSelection(1)
	case keys.Enter:
		sessionID, ok := model.selectedResumeSession()
		if !ok {
			return tuiAction{}, true
		}
		return tuiAction{kind: tuiActionResume, text: sessionID}, true
	case keys.Escape:
		model.dismissResumePicker()
	case keys.EOF, keys.CtrlC, keys.CtrlD:
		return tuiAction{}, false
	default:
		return tuiAction{}, true
	}
	return tuiAction{}, true
}

func reduceCommandPickerKey(model *tuiModel, key keys.Event) bool {
	if !model.commandPickerVisible() {
		return false
	}

	switch key.Code {
	case keys.Up:
		model.moveCommandPickerSelection(-1)
	case keys.Down:
		model.moveCommandPickerSelection(1)
	case keys.Tab:
		if err := model.applyCommandPickerSelection(); err != nil {
			setInputError(model, err)
		}
	case keys.Enter:
		selected := model.commandPicker.matches[model.commandPicker.selected]
		if selected.text == model.commandPicker.query {
			return false
		}
		if err := model.applyCommandPickerSelection(); err != nil {
			setInputError(model, err)
			return true
		}
		return false
	case keys.Escape:
		model.dismissCommandPicker()
	default:
		return false
	}
	return true
}

func reduceFilePickerKey(model *tuiModel, key keys.Event) bool {
	if key.Code == keys.Escape && model.filePicker.active {
		model.dismissFilePicker()
		return true
	}
	if !model.filePickerVisible() {
		return false
	}
	if !model.filePicker.matchesCurrent {
		return key.Code == keys.Up || key.Code == keys.Down || key.Code == keys.Enter || key.Code == keys.Tab || key.Code == keys.Right
	}
	if len(model.filePicker.matches) == 0 {
		return model.filePicker.state == filesearch.StateDiscovering && (key.Code == keys.Up || key.Code == keys.Down || key.Code == keys.Enter || key.Code == keys.Tab || key.Code == keys.Right)
	}

	switch key.Code {
	case keys.Up:
		model.moveFilePickerSelection(-1)
	case keys.Down:
		model.moveFilePickerSelection(1)
	case keys.Enter:
		if err := model.applyFilePickerSelection(); err != nil {
			setInputError(model, err)
		}
	case keys.Tab:
		match := model.filePicker.matches[model.filePicker.selected]
		var err error
		if match.IsDir {
			err = model.drillIntoFilePickerDirectory()
		} else {
			err = model.applyFilePickerSelection()
		}
		if err != nil {
			setInputError(model, err)
		}
	case keys.Right:
		match := model.filePicker.matches[model.filePicker.selected]
		if !match.IsDir {
			return false
		}
		if err := model.drillIntoFilePickerDirectory(); err != nil {
			setInputError(model, err)
		}
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
	return tuiAction{kind: tuiActionCancel}, nil
}

func reduceSubmission(model *tuiModel) tuiAction {
	content, ok := model.submittingContent()
	if !ok {
		return tuiAction{}
	}

	trimmed := strings.TrimSpace(contentText(content))
	if contentHasImage(content) || !strings.HasPrefix(trimmed, "/") {
		if !model.running {
			return tuiAction{kind: tuiActionSubmit, content: content}
		}
		model.finishSubmission(content)
		return tuiAction{kind: tuiActionSteer, content: content}
	}

	command, arg, ok := findCommand(model.commands, trimmed)
	switch {
	case ok && (command.duringRun || !model.running):
		model.finishSubmission(content)
		return tuiAction{kind: tuiActionCommand, command: command, text: arg}
	case model.running:
		setInputError(model, errors.New("commands cannot be queued while the agent is running"))
	default:
		model.finishSubmission(content)
		model.appendBlock(blockError, "Unknown command "+singleLine(trimmed, 120))
		model.activity = activity{kind: activityError, detail: "unknown command"}
	}
	return tuiAction{}
}

func contentHasImage(content []agent.ContentPart) bool {
	for _, part := range content {
		if part.Kind == agent.ContentPartImage {
			return true
		}
	}
	return false
}

func setInputError(model *tuiModel, err error) {
	detail := singleLine(err.Error(), 200)
	model.activity = activity{kind: activityError, detail: detail}
}
