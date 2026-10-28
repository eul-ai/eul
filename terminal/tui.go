package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/filesearch"
	"github.com/eul-ai/eul/terminal/keys"
)

const (
	enableMouse          = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	disableMouse         = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
	enterScreen          = "\x1b[?1049h\x1b[?2004h\x1b[>1u" + enableMouse + "\x1b[2J\x1b[H"
	leaveScreen          = ansiEndSynchronizedOutput + disableMouse + "\x1b[<u\x1b[?2004l\x1b[?25h\x1b[?1049l"
	providerUsageTimeout = 10 * time.Second
	renderDelay          = time.Second / 60
)

type providerUsageMessage struct {
	usage backend.AccountUsage
	err   error
}

type Runner struct {
	output    io.Writer
	inputFD   int
	outputFD  int
	state     *terminalState
	keys      chan keys.Event
	stopped   chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func NewRunner(input io.Reader, output io.Writer) (*Runner, error) {
	inputFD, inputOK := descriptor(input)
	outputFD, outputOK := descriptor(output)
	if !inputOK || !outputOK || !isTerminal(inputFD) || !isTerminal(outputFD) {
		return nil, errNotTerminal
	}
	if _, _, err := terminalSize(outputFD); err != nil {
		return nil, fmt.Errorf("terminal: get size: %w", err)
	}
	state, err := makeRaw(inputFD)
	if err != nil {
		return nil, fmt.Errorf("terminal: enter raw mode: %w", err)
	}
	if err := writeOutput(output, "%s", enterScreen); err != nil {
		leaveErr := writeOutput(output, "%s", leaveScreen)
		restoreErr := restoreTerminal(inputFD, state)
		return nil, errors.Join(err, leaveErr, wrapRestoreError(restoreErr))
	}

	runner := &Runner{
		output:   output,
		inputFD:  inputFD,
		outputFD: outputFD,
		state:    state,
		keys:     make(chan keys.Event, 64),
		stopped:  make(chan struct{}),
	}
	go keys.Read(input, runner.keys, runner.stopped)
	return runner, nil
}

func (runner *Runner) Run(ctx context.Context, options Options) (RunOutcome, error) {
	width, height, err := terminalSize(runner.outputFD)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("terminal: get size: %w", err)
	}
	if err := setTerminalTitle(runner.output, options.Config.WorkingDirectory); err != nil {
		return RunOutcome{}, err
	}
	return runner.run(ctx, options, width, height)
}

func setTerminalTitle(output io.Writer, workingDirectory string) error {
	return writeOutput(output, "\x1b]2;ℯ - %s\x07", replaceControls(filepath.Base(workingDirectory)))
}

func (runner *Runner) Close() error {
	if runner == nil {
		return nil
	}
	runner.closeOnce.Do(func() {
		close(runner.stopped)
		leaveErr := writeOutput(runner.output, "%s", leaveScreen)
		restoreErr := restoreTerminal(runner.inputFD, runner.state)
		runner.closeErr = errors.Join(leaveErr, wrapRestoreError(restoreErr))
	})
	return runner.closeErr
}

func wrapRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("terminal: restore: %w", err)
}

func (runner *Runner) run(ctx context.Context, options Options, width, height int) (RunOutcome, error) {
	model := newTUIModel(width, height, options)
	fileSearch := filesearch.New(options.Config.WorkingDirectory)
	defer fileSearch.Close()
	fileSearchResults := fileSearch.Results()
	clipboardImages := make(chan tuiEvent, maxAttachedImages)
	usageResults := make(chan providerUsageMessage, 1)

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

	controller := newTUIController(controllerOptions{
		model:           model,
		output:          runner.output,
		outputFD:        runner.outputFD,
		stopped:         runner.stopped,
		fileSearch:      fileSearch,
		loadUsage:       options.Services.LoadUsage,
		usageResults:    usageResults,
		clipboardImages: clipboardImages,
		controls:        options.Controls,
		sessions:        options.Sessions,
		messageHistory:  options.MessageHistory,
	})
	defer controller.cancelClipboardRequests()
	defer controller.usageLoads.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	controller.requestUsage(ctx)
	if _, err := controller.transition(ctx, tuiEvent{kind: tuiEventRender}); err != nil {
		return RunOutcome{}, err
	}
	if options.Config.InitialPrompt != "" {
		done, err := submitInitialPrompt(ctx, controller, model, options.Config.InitialPrompt)
		if err != nil {
			return RunOutcome{}, err
		}
		if done {
			return controller.outcome, nil
		}
	}

	interrupts := options.Events.Interrupts
	updates := options.Session.Updates()
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
		case key := <-runner.keys:
			event = tuiEvent{kind: tuiEventKey, key: key}
		case update := <-updates:
			event = tuiEvent{kind: tuiEventSession, update: update}
		case message := <-usageResults:
			event = tuiEvent{kind: tuiEventProviderUsage, providerUsage: message}
		case event = <-clipboardImages:
		case result := <-fileSearchResults:
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

// submitInitialPrompt sends the prompt the session was started with as its first
// message, exactly as if it had been typed and entered.
func submitInitialPrompt(ctx context.Context, controller *tuiController, model *tuiModel, prompt string) (bool, error) {
	if err := model.insertInput(prompt); err != nil {
		return false, err
	}
	return controller.transition(ctx, tuiEvent{kind: tuiEventKey, key: keys.Event{Code: keys.Enter}})
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
