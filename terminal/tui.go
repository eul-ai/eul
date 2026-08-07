package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/term"

	"yaah/agent"
)

const (
	enterScreen          = "\x1b[?1049h\x1b[?2004h\x1b[>1u\x1b[2J\x1b[H"
	leaveScreen          = ansiEndSynchronizedOutput + "\x1b[<u\x1b[?2004l\x1b[?25h\x1b[?1049l"
	providerUsageTimeout = 10 * time.Second
)

type engineMessage struct {
	event *agent.Event
	err   error
	done  bool
}

type providerUsageMessage struct {
	usage agent.ProviderUsage
	err   error
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
		runErr = errors.Join(runErr, leaveErr, wrapRestoreError(restoreErr))
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

	usageContext, cancelUsage := context.WithCancel(ctx)
	defer cancelUsage()
	var usageRequests chan struct{}
	var usageMessages <-chan providerUsageMessage
	if options.LoadUsage != nil {
		requests := make(chan struct{}, 1)
		messages := make(chan providerUsageMessage, 1)
		usageRequests = requests
		usageMessages = messages
		go loadProviderUsage(usageContext, options.LoadUsage, requests, messages)
		requestProviderUsage(usageRequests)
	}

	resizes, stopResizes := watchResize()
	defer stopResizes()

	renderTicker := time.NewTicker(time.Second / 30)
	defer renderTicker.Stop()
	spinnerTicker := time.NewTicker(80 * time.Millisecond)
	defer spinnerTicker.Stop()
	var usageClock <-chan time.Time
	if options.LoadUsage != nil {
		usageTicker := time.NewTicker(time.Minute)
		defer usageTicker.Stop()
		usageClock = usageTicker.C
	}

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
				cancelActiveTurn(turnCancel, engineMessages)
				return fmt.Errorf("terminal: get size: %w", err)
			}
			model.width = newWidth
			model.height = newHeight
			dirty = true

		case key := <-keys:
			exit, err := handleKey(ctx, model, engine, key, engineMessages, stopped, &turnCancel)
			if err != nil {
				cancelActiveTurn(turnCancel, engineMessages)
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
				return contextErr
			}
			if exitAfterTurn != nil {
				if errors.Is(exitAfterTurn, io.EOF) {
					return nil
				}
				return exitAfterTurn
			}
			model.finishTurn(message.err)
			requestProviderUsage(usageRequests)
			dirty = true

		case message := <-usageMessages:
			if message.err == nil {
				model.providerUsage = agent.ProviderUsage{Windows: append([]agent.UsageWindow(nil), message.usage.Windows...)}
				dirty = true
			}

		case <-spinnerTicker.C:
			if model.activity.kind != activityReady && model.activity.kind != activityError {
				model.spinner++
				dirty = true
			}

		case <-usageClock:
			for _, window := range model.providerUsage.Windows {
				if !window.ResetsAt.IsZero() {
					dirty = true
					break
				}
			}

		case <-renderTicker.C:
			if err := renderIfDirty(renderer, model, options.Output, &dirty); err != nil {
				cancelActiveTurn(turnCancel, engineMessages)
				return err
			}
		}
	}
}

func requestProviderUsage(requests chan<- struct{}) {
	select {
	case requests <- struct{}{}:
	default:
	}
}

func loadProviderUsage(
	ctx context.Context,
	load func(context.Context) (agent.ProviderUsage, error),
	requests <-chan struct{},
	messages chan<- providerUsageMessage,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-requests:
		}

		requestContext, cancel := context.WithTimeout(ctx, providerUsageTimeout)
		usage, err := load(requestContext)
		cancel()

		select {
		case messages <- providerUsageMessage{usage: usage, err: err}:
		case <-ctx.Done():
			return
		}
	}
}

func cancelActiveTurn(cancel context.CancelFunc, messages <-chan engineMessage) {
	if cancel == nil {
		return
	}

	cancel()
	for {
		message := <-messages
		if message.done {
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
	action, err := reduceInterrupt(model)
	if err != nil {
		return err
	}
	if action.kind == tuiActionCancel {
		cancel()
	}
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
	action, err := reduceKey(model, key)
	if err != nil {
		return false, err
	}

	switch action.kind {
	case tuiActionNone:
		return false, nil
	case tuiActionCancel:
		(*turnCancel)()
	case tuiActionReset:
		engine.Reset()
		model.clearConversation()
	case tuiActionExit:
		return true, nil
	case tuiActionSubmit:
		turnContext, cancel := context.WithCancel(ctx)
		*turnCancel = cancel
		go runEngineTurn(turnContext, engine, action.prompt, messages, stopped)
	case tuiActionSetThinking:
		if err := model.setThinkingLevel(action.thinkingLevel); err != nil {
			setInputError(model, err)
			return false, nil
		}
		model.thinkingLevel = action.thinkingLevel
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
