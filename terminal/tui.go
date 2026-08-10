package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/term"

	"github.com/eul-ai/eul/agent"
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
	ack   chan error
}

type providerUsageMessage struct {
	usage agent.ProviderUsage
	err   error
}

type Runner struct {
	input     io.Reader
	output    io.Writer
	inputFD   int
	outputFD  int
	state     *term.State
	keys      chan keyEvent
	stopped   chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func NewRunner(input io.Reader, output io.Writer) (*Runner, error) {
	inputFD, inputOK := descriptor(input)
	outputFD, outputOK := descriptor(output)
	if !inputOK || !outputOK || !term.IsTerminal(inputFD) || !term.IsTerminal(outputFD) {
		return nil, ErrNotTerminal
	}
	if _, _, err := term.GetSize(outputFD); err != nil {
		return nil, fmt.Errorf("terminal: get size: %w", err)
	}
	state, err := term.MakeRaw(inputFD)
	if err != nil {
		return nil, fmt.Errorf("terminal: enter raw mode: %w", err)
	}
	if err := writeOutput(output, "%s", enterScreen); err != nil {
		leaveErr := writeOutput(output, "%s", leaveScreen)
		restoreErr := term.Restore(inputFD, state)
		return nil, errors.Join(err, leaveErr, wrapRestoreError(restoreErr))
	}

	runner := &Runner{
		input:    input,
		output:   output,
		inputFD:  inputFD,
		outputFD: outputFD,
		state:    state,
		keys:     make(chan keyEvent, 64),
		stopped:  make(chan struct{}),
	}
	go readKeyEvents(input, runner.keys, runner.stopped)
	return runner, nil
}

func (runner *Runner) Run(ctx context.Context, engine Engine, options Options) error {
	width, height, err := term.GetSize(runner.outputFD)
	if err != nil {
		return fmt.Errorf("terminal: get size: %w", err)
	}
	if err := setTerminalTitle(runner.output, options.WorkingDirectory); err != nil {
		return err
	}
	options.Input = runner.input
	options.Output = runner.output
	return runTUIWithKeys(ctx, engine, options, runner.outputFD, width, height, runner.keys, runner.stopped)
}

func setTerminalTitle(output io.Writer, workingDirectory string) error {
	directory := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, filepath.Base(workingDirectory))

	return writeOutput(output, "\x1b]2;ℇ - %s\x07", directory)
}

func (runner *Runner) Close() error {
	if runner == nil {
		return nil
	}
	runner.closeOnce.Do(func() {
		close(runner.stopped)
		leaveErr := writeOutput(runner.output, "%s", leaveScreen)
		restoreErr := term.Restore(runner.inputFD, runner.state)
		runner.closeErr = errors.Join(leaveErr, wrapRestoreError(restoreErr))
	})
	return runner.closeErr
}

func Run(ctx context.Context, engine Engine, options Options) error {
	runner, err := NewRunner(options.Input, options.Output)
	if err != nil {
		return err
	}
	return errors.Join(runner.Run(ctx, engine, options), runner.Close())
}

func wrapRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("terminal: restore: %w", err)
}

func runTUI(ctx context.Context, engine Engine, options Options, outputFD, width, height int) error {
	keys := make(chan keyEvent, 64)
	stopped := make(chan struct{})
	defer close(stopped)
	go readKeyEvents(options.Input, keys, stopped)
	return runTUIWithKeys(ctx, engine, options, outputFD, width, height, keys, stopped)
}

func runTUIWithKeys(
	ctx context.Context,
	engine Engine,
	options Options,
	outputFD int,
	width int,
	height int,
	keys <-chan keyEvent,
	stopped <-chan struct{},
) error {
	model := newTUIModel(width, height, options)
	fileSearch := newFileSearchRunner(options.WorkingDirectory)
	defer fileSearch.close()
	fileSearchMessages := make(chan fileSearchResult, 64)
	engineMessages := make(chan engineMessage, 256)

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
		saveCheckpoint:     options.SaveCheckpoint,
		listSessions:       options.ListSessions,
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
	runEngineOperation(ctx, messages, stopped, func(sink agent.EventSink) error {
		_, err := engine.Run(ctx, prompt, sink)
		return err
	})
}

func runEngineCompaction(ctx context.Context, engine Engine, messages chan<- engineMessage, stopped <-chan struct{}) {
	runEngineOperation(ctx, messages, stopped, func(sink agent.EventSink) error {
		return engine.Compact(ctx, sink)
	})
}

func runEngineOperation(ctx context.Context, messages chan<- engineMessage, stopped <-chan struct{}, operation func(agent.EventSink) error) {
	err := operation(func(event agent.Event) error {
		message := engineMessage{event: &event}
		if event.Kind == agent.EventCheckpoint {
			message.ack = make(chan error, 1)
		}
		select {
		case messages <- message:
		case <-ctx.Done():
			return ctx.Err()
		case <-stopped:
			return context.Canceled
		}
		if message.ack == nil {
			return nil
		}
		select {
		case err := <-message.ack:
			return err
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
