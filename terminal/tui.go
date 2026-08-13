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
	"github.com/eul-ai/eul/terminal/clipboard"
)

const (
	enableMouse          = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	disableMouse         = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
	enterScreen          = "\x1b[?1049h\x1b[?2004h\x1b[>1u" + enableMouse + "\x1b[2J\x1b[H"
	leaveScreen          = ansiEndSynchronizedOutput + disableMouse + "\x1b[<u\x1b[?2004l\x1b[?25h\x1b[?1049l"
	providerUsageTimeout = 10 * time.Second
	renderDelay          = time.Second / 60
)

type engineMessage struct {
	event *agent.Event
	err   error
	done  bool
	ack   chan error
}

type providerUsageMessage struct {
	usage ProviderUsage
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

func (runner *Runner) Run(ctx context.Context, engine Engine, options Options) (RunOutcome, error) {
	if err := validateCheckpointCapability(engine, canSaveCheckpoint(options.Persistence)); err != nil {
		return RunOutcome{}, err
	}

	width, height, err := term.GetSize(runner.outputFD)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("terminal: get size: %w", err)
	}
	if err := setTerminalTitle(runner.output, options.Config.WorkingDirectory); err != nil {
		return RunOutcome{}, err
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

	return writeOutput(output, "\x1b]2;ℯ - %s\x07", directory)
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

func Run(ctx context.Context, engine Engine, options Options) (RunOutcome, error) {
	if err := validateCheckpointCapability(engine, canSaveCheckpoint(options.Persistence)); err != nil {
		return RunOutcome{}, err
	}

	runner, err := NewRunner(options.Input, options.Output)
	if err != nil {
		return RunOutcome{}, err
	}
	outcome, runErr := runner.Run(ctx, engine, options)
	if closeErr := runner.Close(); closeErr != nil {
		return RunOutcome{}, closeErr
	}
	return outcome, runErr
}

func wrapRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("terminal: restore: %w", err)
}

func runTUI(ctx context.Context, engine Engine, options Options, outputFD, width, height int) (RunOutcome, error) {
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
) (RunOutcome, error) {
	model := newTUIModel(width, height, options)
	fileSearch := newFileSearchRunner(options.Config.WorkingDirectory)
	defer fileSearch.close()
	fileSearchMessages := make(chan fileSearchResult, 64)
	engineMessages := make(chan engineMessage, 256)
	clipboardImages := make(chan tuiEvent, maxAttachedImages)

	usageContext, cancelUsage := context.WithCancel(ctx)
	var usageDone <-chan struct{}
	defer func() {
		cancelUsage()
		if usageDone != nil {
			<-usageDone
		}
	}()
	var usageRequests chan struct{}
	var usageMessages <-chan providerUsageMessage
	if options.Services.LoadUsage != nil {
		requests := make(chan struct{}, 1)
		messages := make(chan providerUsageMessage, 1)
		done := make(chan struct{})
		usageRequests = requests
		usageMessages = messages
		usageDone = done
		go func() {
			defer close(done)
			loadProviderUsage(usageContext, options.Services.LoadUsage, requests, messages)
		}()
		requestProviderUsage(usageRequests)
	}

	resizes, stopResizes := watchResize()
	defer stopResizes()

	renderTimer := time.NewTimer(renderDelay)
	renderTimer.Stop()
	defer renderTimer.Stop()
	var renderClock <-chan time.Time

	spinnerTicker := time.NewTicker(80 * time.Millisecond)
	defer spinnerTicker.Stop()
	var usageClock <-chan time.Time
	if options.Services.LoadUsage != nil {
		usageTicker := time.NewTicker(time.Minute)
		defer usageTicker.Stop()
		usageClock = usageTicker.C
	}

	clipboardImageReader := options.Services.ReadClipboardImage
	if clipboardImageReader == nil {
		clipboardImageReader = clipboard.ReadImage
	}
	var setThinkingLevel func(agent.ThinkingLevel) error
	if canSetThinkingLevel(options.Commands) {
		setThinkingLevel = options.Commands.SetThinkingLevel
	}
	var setFastMode func(bool) error
	if canSetFastMode(options.Commands) {
		setFastMode = options.Commands.SetFastMode
	}
	var saveCheckpoint func(agent.Checkpoint, Checkpoint, bool) error
	if canSaveCheckpoint(options.Persistence) {
		saveCheckpoint = options.Persistence.SaveCheckpoint
	}
	var listSessions func(context.Context) ([]SessionSummary, []string, error)
	if canListSessions(options.Persistence) {
		listSessions = options.Persistence.ListSessions
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
		setThinkingLevel:   setThinkingLevel,
		setFastMode:        setFastMode,
		saveCheckpoint:     saveCheckpoint,
		listSessions:       listSessions,
		readClipboardImage: clipboardImageReader,
		clipboardImages:    clipboardImages,
		clipboardRequests:  make(map[uint64]context.CancelFunc),
		dirty:              true,
	}
	model.steeringView = &controller.steering
	defer controller.cancelClipboardRequests()
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventRender}); err != nil {
		return RunOutcome{}, err
	}

	interrupts := options.Events.Interrupts
	subagentUpdates := options.Events.SubagentUpdates
	permissionRequests := options.Events.PermissionRequests
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
		case status, ok := <-subagentUpdates:
			if !ok {
				subagentUpdates = nil
				continue
			}
			event = tuiEvent{kind: tuiEventSubagentStatus, subagentStatus: status}
		case request, ok := <-permissionRequests:
			if !ok {
				permissionRequests = nil
				continue
			}
			event = tuiEvent{kind: tuiEventPermission, permission: request}
		case event = <-clipboardImages:
		case result := <-fileSearchMessages:
			event = tuiEvent{kind: tuiEventFileSearch, fileSearch: result}
		case <-spinnerTicker.C:
			event = tuiEvent{kind: tuiEventSpinner}
		case <-usageClock:
			event = tuiEvent{kind: tuiEventUsageClock}
		case <-renderClock:
			renderClock = nil
			event = tuiEvent{kind: tuiEventRender}
		}

		done, err := controller.transition(ctx, event)
		if err != nil {
			cancelActiveTurn(controller.turnCancel, engineMessages)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RunOutcome{}, ctxErr
			}
			return RunOutcome{}, err
		}
		if done {
			return controller.outcome, nil
		}
		if controller.dirty && renderClock == nil {
			renderTimer.Reset(renderDelay)
			renderClock = renderTimer.C
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
	load func(context.Context) (ProviderUsage, error),
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

	renderer.normalizeViewport(model)
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

func runEngineTurn(ctx context.Context, engine Engine, content []agent.ContentPart, messages chan<- engineMessage, stopped <-chan struct{}) {
	runEngineOperation(ctx, messages, stopped, func(sink agent.EventSink) error {
		_, err := engine.RunContent(ctx, content, sink)
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
