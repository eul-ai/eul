package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/term"

	"yaah/agent"
)

const (
	enterScreen = "\x1b[?1049h\x1b[?2004h\x1b[2J\x1b[H"
	leaveScreen = ansiEndSynchronizedOutput + "\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

type engineMessage struct {
	event *agent.Event
	err   error
	done  bool
}

func Run(ctx context.Context, engine Engine, options Options) (runErr error) {
	inputFD, inputOK := descriptor(options.Input)
	outputFD, outputOK := descriptor(options.Output)
	if !inputOK || !outputOK || !term.IsTerminal(inputFD) || !term.IsTerminal(outputFD) {
		return ErrNotTerminal
	}

	width, height, err := term.GetSize(outputFD)
	if err != nil {
		return fmt.Errorf("terminal: get size: %w", err)
	}
	state, err := term.MakeRaw(inputFD)
	if err != nil {
		return fmt.Errorf("terminal: enter raw mode: %w", err)
	}
	defer func() {
		leaveErr := writeOutput(options.Output, "%s", leaveScreen)
		restoreErr := term.Restore(inputFD, state)
		cleanupErr := errors.Join(leaveErr, wrapRestoreError(restoreErr))
		if cleanupErr == nil {
			return
		}
		if runErr != nil {
			runErr = fmt.Errorf("%v; cleanup: %w", runErr, cleanupErr)
			return
		}
		runErr = cleanupErr
	}()

	if err := writeOutput(options.Output, "%s", enterScreen); err != nil {
		return err
	}
	return runTUI(ctx, engine, options, outputFD, width, height)
}

func wrapRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("terminal: restore: %w", err)
}

func runTUI(ctx context.Context, engine Engine, options Options, outputFD, width, height int) error {
	model := newTUIModel(width, height, options)
	keys := make(chan keyEvent, 64)
	engineMessages := make(chan engineMessage, 256)
	stopped := make(chan struct{})
	defer close(stopped)
	go readKeyEvents(options.Input, keys, stopped)

	resizes, stopResizes := watchResize()
	defer stopResizes()

	renderTicker := time.NewTicker(time.Second / 30)
	defer renderTicker.Stop()
	spinnerTicker := time.NewTicker(80 * time.Millisecond)
	defer spinnerTicker.Stop()

	renderer := &tuiRenderer{}
	dirty := true
	if err := renderIfDirty(renderer, model, options.Output, &dirty); err != nil {
		return err
	}

	interrupts := options.Interrupts
	parentDone := ctx.Done()
	var turnCancel context.CancelFunc
	var exitAfterTurn error

	for {
		select {
		case <-parentDone:
			parentDone = nil
			if !model.running {
				return ctx.Err()
			}
			exitAfterTurn = ctx.Err()
			turnCancel()
			model.activity = activity{kind: activityCanceling}
			dirty = true

		case _, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			if err := interruptTUI(model, turnCancel); err != nil {
				return err
			}
			dirty = true

		case <-resizes:
			newWidth, newHeight, err := term.GetSize(outputFD)
			if err != nil {
				cancelActiveTurn(turnCancel, engineMessages, engine)
				return fmt.Errorf("terminal: get size: %w", err)
			}
			model.width = newWidth
			model.height = newHeight
			dirty = true

		case key := <-keys:
			exit, err := handleKey(ctx, model, engine, key, engineMessages, stopped, &turnCancel)
			if err != nil {
				cancelActiveTurn(turnCancel, engineMessages, engine)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return err
			}
			if exit {
				if !model.running {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return ctxErr
					}
					return nil
				}
				if exitAfterTurn == nil {
					exitAfterTurn = io.EOF
					turnCancel()
					model.activity = activity{kind: activityCanceling}
				}
			}
			dirty = true

		case message := <-engineMessages:
			if !message.done {
				model.applyAgentEvent(*message.event)
				dirty = true
				continue
			}

			if turnCancel != nil {
				turnCancel()
				turnCancel = nil
			}
			if contextErr := ctx.Err(); contextErr != nil {
				resetIfNeeded(engine)
				return contextErr
			}
			if exitAfterTurn != nil {
				resetIfNeeded(engine)
				if errors.Is(exitAfterTurn, io.EOF) {
					return nil
				}
				return exitAfterTurn
			}
			model.finishTurn(message.err, engine)
			dirty = true

		case <-spinnerTicker.C:
			if model.activity.kind != activityReady && model.activity.kind != activityError {
				model.spinner++
				dirty = true
			}

		case <-renderTicker.C:
			if err := renderIfDirty(renderer, model, options.Output, &dirty); err != nil {
				cancelActiveTurn(turnCancel, engineMessages, engine)
				return err
			}
		}
	}
}

func cancelActiveTurn(cancel context.CancelFunc, messages <-chan engineMessage, engine Engine) {
	if cancel == nil {
		return
	}

	cancel()
	for {
		message := <-messages
		if message.done {
			resetIfNeeded(engine)
			return
		}
	}
}

func renderIfDirty(renderer *tuiRenderer, model *tuiModel, output io.Writer, dirty *bool) error {
	if !*dirty {
		return nil
	}
	frame := renderer.render(model)
	if frame != "" {
		if err := writeOutput(output, "%s", frame); err != nil {
			return err
		}
	}
	*dirty = false
	return nil
}

func interruptTUI(model *tuiModel, cancel context.CancelFunc) error {
	if !model.running {
		return ErrInterrupted
	}
	if model.interrupted {
		return nil
	}

	model.interrupted = true
	model.activity = activity{kind: activityCanceling}
	cancel()
	return nil
}

func handleKey(
	ctx context.Context,
	model *tuiModel,
	engine Engine,
	key keyEvent,
	messages chan<- engineMessage,
	stopped <-chan struct{},
	turnCancel *context.CancelFunc,
) (bool, error) {
	switch key.code {
	case keyFailure:
		if key.fatal {
			return false, fmt.Errorf("terminal: read input: %w", key.err)
		}
		if model.running {
			return false, nil
		}
		detail := diagnostic(key.err.Error(), 200)
		model.activity = activity{kind: activityError, detail: detail}
		return false, nil
	case keyEOF:
		return true, nil
	case keyCtrlC:
		return false, interruptTUI(model, *turnCancel)
	case keyCtrlL:
		model.forceRedraw = true
		return false, nil
	case keyPageUp:
		scrollConversation(model, -1)
		return false, nil
	case keyPageDown:
		scrollConversation(model, 1)
		return false, nil
	}

	if model.running {
		return false, nil
	}

	switch key.code {
	case keyText:
		if err := model.insertInput(key.text); err != nil {
			detail := diagnostic(err.Error(), 200)
			model.activity = activity{kind: activityError, detail: detail}
		}
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
		return len(model.input) == 0, nil
	case keyEnter:
		prompt, ok := model.takePrompt()
		if !ok {
			return false, nil
		}
		trimmed := strings.TrimSpace(prompt)
		switch trimmed {
		case "/help":
			model.appendBlock(blockInfo, "Commands:\n  /help   show this help\n  /clear  discard conversation state\n  /exit   exit yaah")
		case "/clear":
			engine.Reset()
			model.clearConversation()
		case "/exit":
			return true, nil
		default:
			if strings.HasPrefix(trimmed, "/") {
				model.appendBlock(blockError, "Unknown command "+diagnostic(trimmed, 120))
				model.activity = activity{kind: activityError, detail: "unknown command"}
				return false, nil
			}

			model.beginTurn(prompt)
			turnContext, cancel := context.WithCancel(ctx)
			*turnCancel = cancel
			go runEngineTurn(turnContext, engine, prompt, messages, stopped)
		}
	}
	return false, nil
}

func runEngineTurn(ctx context.Context, engine Engine, prompt string, messages chan<- engineMessage, stopped <-chan struct{}) {
	_, err := engine.Run(ctx, prompt, func(event agent.Event) error {
		select {
		case messages <- engineMessage{event: &event}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-stopped:
			return context.Canceled
		}
	})
	select {
	case messages <- engineMessage{err: err, done: true}:
	case <-stopped:
	}
}
