package terminal

import (
	"fmt"
	"strings"

	"yaah/agent"
)

type tuiActionKind uint8

const (
	tuiActionNone tuiActionKind = iota
	tuiActionCancel
	tuiActionReset
	tuiActionExit
	tuiActionSubmit
	tuiActionSetThinking
)

type tuiAction struct {
	kind          tuiActionKind
	prompt        string
	thinkingLevel agent.ThinkingLevel
}

func reduceKey(model *tuiModel, key keyEvent) (tuiAction, error) {
	switch key.code {
	case keyFailure:
		if key.fatal {
			return tuiAction{}, fmt.Errorf("terminal: read input: %w", key.err)
		}
		if model.running {
			return tuiAction{}, nil
		}
		setInputError(model, key.err)
		return tuiAction{}, nil
	case keyEOF:
		return tuiAction{kind: tuiActionExit}, nil
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
		model.forceRedraw = true
		return tuiAction{}, nil
	case keyPageUp:
		scrollConversation(model, -1)
		return tuiAction{}, nil
	case keyPageDown:
		scrollConversation(model, 1)
		return tuiAction{}, nil
	}

	if model.running {
		return tuiAction{}, nil
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
		model.cursor = 0
	case keyEnd:
		model.cursor = len(model.input)
	case keyBackspace:
		model.backspace()
	case keyDelete:
		model.delete()
	case keyUp:
		model.historyUp()
	case keyDown:
		model.historyDown()
	case keyCtrlD:
		if len(model.input) == 0 {
			return tuiAction{kind: tuiActionExit}, nil
		}
	case keyEnter:
		return reducePrompt(model), nil
	}
	return tuiAction{}, nil
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

func reducePrompt(model *tuiModel) tuiAction {
	prompt, ok := model.takePrompt()
	if !ok {
		return tuiAction{}
	}

	trimmed := strings.TrimSpace(prompt)
	switch trimmed {
	case "/help":
		model.appendBlock(blockInfo, "Commands:\n  /help   show this help\n  /clear  discard conversation state\n  /exit   exit yaah")
	case "/clear":
		return tuiAction{kind: tuiActionReset}
	case "/exit":
		return tuiAction{kind: tuiActionExit}
	default:
		if strings.HasPrefix(trimmed, "/") {
			model.appendBlock(blockError, "Unknown command "+diagnostic(trimmed, 120))
			model.activity = activity{kind: activityError, detail: "unknown command"}
			return tuiAction{}
		}

		model.beginTurn(prompt)
		return tuiAction{kind: tuiActionSubmit, prompt: prompt}
	}
	return tuiAction{}
}

func setInputError(model *tuiModel, err error) {
	detail := diagnostic(err.Error(), 200)
	model.activity = activity{kind: activityError, detail: detail}
}
