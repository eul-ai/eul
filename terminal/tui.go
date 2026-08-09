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
	enableMouse          = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	disableMouse         = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
	enterScreen          = "\x1b[?1049h\x1b[?2004h\x1b[>1u" + enableMouse + "\x1b[2J\x1b[H"
	leaveScreen          = ansiEndSynchronizedOutput + disableMouse + "\x1b[<u\x1b[?2004l\x1b[?25h\x1b[?1049l"
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
	fileSearch := newFileSearchRunner(options.WorkingDirectory)
	defer fileSearch.close()
	fileSearchMessages := make(chan fileSearchResult, 64)

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

	renderTicker := time.NewTicker(time.Second / 60)
	defer renderTicker.Stop()
	spinnerTicker := time.NewTicker(80 * time.Millisecond)
	defer spinnerTicker.Stop()
	var usageClock <-chan time.Time
	if options.LoadUsage != nil {
		usageTicker := time.NewTicker(time.Minute)
		defer usageTicker.Stop()
		usageClock = usageTicker.C
	}

	controller := &tuiController{
		model:              model,
		renderer:           &tuiRenderer{},
		engine:             engine,
		output:             options.Output,
		outputFD:           outputFD,
		engineMessages:     engineMessages,
		stopped:            stopped,
		fileSearch:         fileSearch,
		fileSearchMessages: fileSearchMessages,
		usageRequests:      usageRequests,
		setThinkingLevel:   options.SetThinkingLevel,
		dirty:              true,
	}
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventRender}); err != nil {
		return err
	}

	interrupts := options.Interrupts
	parentDone := ctx.Done()
	for {
		var event tuiEvent
		select {
		case <-parentDone:
			parentDone = nil
			event = tuiEvent{kind: tuiEventParentCanceled, err: ctx.Err()}
		case _, ok := <-interrupts:
			if !ok {
				interrupts = nil
				continue
			}
			event = tuiEvent{kind: tuiEventInterrupt}
		case <-resizes:
			event = tuiEvent{kind: tuiEventResize}
		case key := <-keys:
			event = tuiEvent{kind: tuiEventKey, key: key}
		case message := <-engineMessages:
			event = tuiEvent{kind: tuiEventEngine, engine: message}
		case message := <-usageMessages:
			event = tuiEvent{kind: tuiEventProviderUsage, providerUsage: message}
		case result := <-fileSearchMessages:
			event = tuiEvent{kind: tuiEventFileSearch, fileSearch: result}
		case <-spinnerTicker.C:
			event = tuiEvent{kind: tuiEventSpinner}
		case <-usageClock:
			event = tuiEvent{kind: tuiEventUsageClock}
		case <-renderTicker.C:
			event = tuiEvent{kind: tuiEventRender}
		}

		done, err := controller.transition(ctx, event)
		if err != nil {
			cancelActiveTurn(controller.turnCancel, engineMessages)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		if done {
			return err
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

func renderIfDirty(renderer *tuiRenderer, model *tuiModel, output io.Writer, dirty *bool, forceRedraw bool) error {
	if !*dirty {
		return nil
	}
	outputFrame, next := renderer.renderPending(model, forceRedraw)
	if outputFrame != "" {
		if err := writeOutput(output, "%s", outputFrame); err != nil {
			return err
		}
	}
	renderer.commit(next)
	*dirty = false
	return nil
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
