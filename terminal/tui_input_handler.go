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
	tuiActionOpenResume
	tuiActionResume
	tuiActionNewSession
	tuiActionCancel
	tuiActionCompact
	tuiActionToggleFast
	tuiActionExit
	tuiActionSubmit
	tuiActionSteer
	tuiActionShowGoal
	tuiActionSetGoal
	tuiActionClearGoal
	tuiActionDequeue
	tuiActionSetThinking
	tuiActionAttachImage
	tuiActionResolvePermission
	tuiActionCopy
	tuiActionRedraw
)

type tuiAction struct {
	kind               tuiActionKind
	prompt             string
	text               string
	content            []agent.ContentPart
	thinkingLevel      agent.ThinkingLevel
	permissionDecision PermissionDecision
}

func handleKeyInput(model *tuiModel, key keyEvent, frame terminalFrame) (tuiAction, error) {
	if model.permission.active() {
		return reducePermissionKey(model, key), nil
	}
	if key.code == keyMouse {
		return reduceMouse(model, key.mouse, frame), nil
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
	case keyCtrlV:
		if model.running {
			setInputError(model, fmt.Errorf("images cannot be attached while the agent is running"))
			return tuiAction{}, nil
		}
		return tuiAction{kind: tuiActionAttachImage}, nil
	case keyPageUp:
		scrollConversation(model, -1, frame)
		return tuiAction{}, nil
	case keyPageDown:
		scrollConversation(model, 1, frame)
		return tuiAction{}, nil
	case keyAltDown:
		scrollConversationToBottom(model, frame)
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
		model.moveHome()
	case keyEnd:
		model.moveEnd()
	case keyBackspace:
		model.backspace()
	case keyDelete:
		model.delete()
	case keyUp:
		if !model.moveUp() {
			model.historyUp()
		}
	case keyDown:
		if !model.moveDown() {
			model.historyDown()
		}
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

func reducePermissionKey(model *tuiModel, key keyEvent) tuiAction {
	switch key.code {
	case keyText:
		switch strings.ToLower(strings.TrimSpace(key.text)) {
		case "a":
			return permissionAction(PermissionAllowOnce)
		case "d":
			return permissionAction(PermissionDenyOnce)
		case "s":
			return permissionAction(PermissionAllowSession)
		}
	case keyLeft:
		if model.permission.selected > PermissionDenyOnce {
			model.permission.selected--
		}
	case keyRight:
		if model.permission.selected < PermissionAllowSession {
			model.permission.selected++
		}
	case keyTab:
		model.permission.selected = (model.permission.selected + 1) % permissionDecisionCount
	case keyShiftTab:
		model.permission.selected = (model.permission.selected + permissionDecisionCount - 1) % permissionDecisionCount
	case keyUp:
		scrollPermission(model, -1)
	case keyDown:
		scrollPermission(model, 1)
	case keyPageUp:
		scrollPermission(model, -permissionDetailCapacityForModel(model))
	case keyPageDown:
		scrollPermission(model, permissionDetailCapacityForModel(model))
	case keyEnter:
		return permissionAction(model.permission.selected)
	case keyEscape:
		return permissionAction(PermissionDenyOnce)
	case keyCtrlC:
		return tuiAction{kind: tuiActionCancel}
	case keyEOF, keyCtrlD:
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

func reduceResumePickerKey(model *tuiModel, key keyEvent) (tuiAction, bool) {
	if !model.resumePickerVisible() {
		return tuiAction{}, false
	}

	switch key.code {
	case keyUp:
		model.moveResumePickerSelection(-1)
	case keyDown:
		model.moveResumePickerSelection(1)
	case keyEnter:
		sessionID, ok := model.selectedResumeSession()
		if !ok {
			return tuiAction{}, true
		}
		return tuiAction{kind: tuiActionResume, text: sessionID}, true
	case keyEscape:
		model.dismissResumePicker()
	case keyEOF, keyCtrlC, keyCtrlD:
		return tuiAction{}, false
	default:
		return tuiAction{}, true
	}
	return tuiAction{}, true
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
	return tuiAction{kind: tuiActionCancel}, nil
}

func reduceSteeringPrompt(model *tuiModel) tuiAction {
	prompt := model.inputText()
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return tuiAction{}
	}
	if strings.HasPrefix(trimmed, "/") {
		action, command, ok := matchSlashCommand(prompt, trimmed, model.fastModeAvailable)
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
	content, ok := model.submittingContent()
	if !ok {
		return tuiAction{}
	}

	prompt := contentText(content)
	trimmed := strings.TrimSpace(prompt)
	if !contentHasImage(content) {
		if action, _, matched := matchSlashCommand(prompt, trimmed, model.fastModeAvailable); matched {
			model.finishSubmission(content)
			return action
		}
		if strings.HasPrefix(trimmed, "/") {
			model.finishSubmission(content)
			model.appendBlock(blockError, "Unknown command "+diagnostic(trimmed, 120))
			model.activity = activity{kind: activityError, detail: "unknown command"}
			return tuiAction{}
		}
	}

	return tuiAction{kind: tuiActionSubmit, prompt: prompt, content: content}
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
	detail := diagnostic(err.Error(), 200)
	model.activity = activity{kind: activityError, detail: detail}
}
