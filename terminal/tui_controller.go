package terminal

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/filesearch"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/clipboard"
	"github.com/eul-ai/eul/terminal/keys"
)

type tuiEventKind uint8

const (
	tuiEventParentCanceled tuiEventKind = iota
	tuiEventInterrupt
	tuiEventResize
	tuiEventKey
	tuiEventEngine
	tuiEventProviderUsage
	tuiEventSubagentStatus
	tuiEventPermission
	tuiEventClipboardImage
	tuiEventFileSearch
	tuiEventSpinner
	tuiEventUsageClock
	tuiEventRender
)

type tuiEvent struct {
	kind           tuiEventKind
	key            keys.Event
	engine         engineMessage
	providerUsage  providerUsageMessage
	subagentStatus subagent.Status
	permission     PermissionRequest
	image          agent.Image
	requestID      uint64
	fileSearch     filesearch.Result
	err            error
}

type controllerOptions struct {
	model           *tuiModel
	output          io.Writer
	outputFD        int
	engineMessages  chan<- engineMessage
	stopped         <-chan struct{}
	fileSearch      *filesearch.Searcher
	loadUsage       func(context.Context) (ProviderUsage, error)
	usageResults    chan<- providerUsageMessage
	clipboardImages chan<- tuiEvent
	operations      Operations
	controls        Controls
	stateChanges    StateChanges
	sessions        Sessions
	messageHistory  MessageHistory
}

func newTUIController(config controllerOptions) *tuiController {
	return &tuiController{
		model:              config.model,
		renderer:           &tuiRenderer{},
		operations:         config.operations,
		controls:           config.controls,
		output:             config.output,
		outputFD:           config.outputFD,
		engineMessages:     config.engineMessages,
		stopped:            config.stopped,
		fileSearch:         config.fileSearch,
		loadUsage:          config.loadUsage,
		usageResults:       config.usageResults,
		stateChanges:       config.stateChanges,
		sessions:           config.sessions,
		messageHistory:     config.messageHistory,
		readClipboardImage: clipboard.ReadImage,
		clipboardImages:    config.clipboardImages,
		clipboardRequests:  make(map[uint64]context.CancelFunc),
		dirty:              true,
	}
}

type tuiController struct {
	model                        *tuiModel
	renderer                     *tuiRenderer
	operations                   Operations
	controls                     Controls
	output                       io.Writer
	outputFD                     int
	engineMessages               chan<- engineMessage
	stopped                      <-chan struct{}
	fileSearch                   *filesearch.Searcher
	loadUsage                    func(context.Context) (ProviderUsage, error)
	usageResults                 chan<- providerUsageMessage
	usageLoading                 bool
	usageLoads                   sync.WaitGroup
	stateChanges                 StateChanges
	sessions                     Sessions
	messageHistory               MessageHistory
	readClipboardImage           func(context.Context) (agent.Image, error)
	clipboardImages              chan<- tuiEvent
	clipboardRequests            map[uint64]context.CancelFunc
	nextClipboardID              uint64
	turnCancel                   context.CancelFunc
	exitAfterTurn                bool
	exitAfterTurnErr             error
	outcome                      RunOutcome
	permissionsAllowedForSession bool
	dirty                        bool
	forceRedraw                  bool
}

func (c *tuiController) transition(ctx context.Context, event tuiEvent) (bool, error) {
	switch event.kind {
	case tuiEventParentCanceled:
		return c.handleParentCanceled(event.err)
	case tuiEventInterrupt:
		return c.handleInterrupt()
	case tuiEventResize:
		return c.handleResize()
	case tuiEventKey:
		return c.handleKey(ctx, event.key)
	case tuiEventEngine:
		return c.handleEngineMessage(ctx, event.engine)
	case tuiEventProviderUsage:
		return c.handleProviderUsage(event.providerUsage)
	case tuiEventSubagentStatus:
		return c.handleSubagentStatus(event.subagentStatus)
	case tuiEventPermission:
		return c.handlePermission(event.permission)
	case tuiEventClipboardImage:
		return c.handleClipboardImage(event.requestID, event.image, event.err)
	case tuiEventFileSearch:
		return c.handleFileSearch(event.fileSearch)
	case tuiEventSpinner:
		return c.handleSpinner()
	case tuiEventUsageClock:
		return c.handleUsageClock(ctx)
	case tuiEventRender:
		return c.handleRender()
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleParentCanceled(parentErr error) (bool, error) {
	c.cancelClipboardRequests()
	if !c.model.running {
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		return true, parentErr
	}

	c.exitAfterTurn = true
	c.exitAfterTurnErr = parentErr
	c.cancelTurn()
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleInterrupt() (bool, error) {
	action, err := reduceInterrupt(c.model)
	if err != nil {
		if checkpointErr := c.saveCurrentCheckpoint(false); checkpointErr != nil {
			return false, checkpointErr
		}
		return false, err
	}
	if action.kind == tuiActionCancel {
		c.interruptTurn()
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleResize() (bool, error) {
	width, height, err := terminalSize(c.outputFD)
	if err != nil {
		return false, fmt.Errorf("terminal: get size: %w", err)
	}
	c.model.width = width
	c.model.height = height
	c.model.selection = textSelection{}
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleKey(ctx context.Context, key keys.Event) (bool, error) {
	pendingBefore := c.model.pendingImageRequests()
	action, err := handleKeyInput(c.model, key, c.renderer.frame)
	if err != nil {
		if !c.model.running {
			if checkpointErr := c.saveCurrentCheckpoint(false); checkpointErr != nil {
				return false, checkpointErr
			}
		}
		return false, err
	}
	c.cancelRemovedClipboardRequests(pendingBefore)

	exit, err := c.applyAction(ctx, action)
	if err != nil {
		return false, err
	}
	if err := c.flushMessageHistory(); err != nil {
		return false, err
	}
	command := c.model.takeFileSearchCommand()
	switch {
	case command.request != nil:
		c.fileSearch.Search(ctx, *command.request)
	case command.cancel:
		c.fileSearch.Cancel()
	}
	if exit {
		if !c.model.running {
			if err := ctx.Err(); err != nil {
				return true, err
			}
			return true, nil
		}
		if !c.exitAfterTurn {
			c.exitAfterTurn = true
			c.cancelTurn()
		}
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) flushMessageHistory() error {
	if c.messageHistory.Append == nil {
		c.model.pendingHistory = nil
		return nil
	}
	for len(c.model.pendingHistory) > 0 {
		prompt := c.model.pendingHistory[0]
		if err := c.messageHistory.Append(prompt); err != nil {
			return fmt.Errorf("save message history: %w", err)
		}
		c.model.pendingHistory[0] = ""
		c.model.pendingHistory = c.model.pendingHistory[1:]
	}
	return nil
}

func (c *tuiController) handleEngineMessage(ctx context.Context, message engineMessage) (bool, error) {
	if message.snapshot != nil {
		message.snapshot <- checkpointModel(c.model, c.model.pendingSteering())
		return false, nil
	}
	if !message.done {
		return c.handleAgentEvent(message)
	}

	if c.turnCancel != nil {
		c.turnCancel()
		c.turnCancel = nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if c.exitAfterTurn {
		return true, c.exitAfterTurnErr
	}

	interrupted := c.model.interrupted
	if interrupted || message.err != nil {
		c.model.restoreSteering(c.model.clearSteering(c.controls.ClearSteering))
	}
	c.model.finishTurn(message.err)
	if err := c.saveCurrentCheckpoint(false); err != nil {
		return false, err
	}
	c.requestUsage(ctx)
	if !interrupted && message.err == nil {
		if err := c.startDeferredTurn(ctx); err != nil {
			return false, err
		}
	}
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleAgentEvent(message engineMessage) (bool, error) {
	if message.event.Kind == agent.EventSteering {
		if c.model.deliverSteering(message.event.Content) {
			c.model.appendUserContent(message.event.Content)
			c.model.setActiveActivity(activity{kind: activityThinking})
		}
	} else {
		c.model.applyAgentEvent(*message.event)
	}
	c.dirty = true
	return false, nil
}

func (c *tuiController) requestUsage(ctx context.Context) {
	if c.loadUsage == nil || c.usageLoading {
		return
	}

	c.usageLoading = true
	load, results := c.loadUsage, c.usageResults
	c.usageLoads.Go(func() {
		loadContext, cancel := context.WithTimeout(ctx, providerUsageTimeout)
		defer cancel()
		usage, err := load(loadContext)
		results <- providerUsageMessage{usage: usage, err: err}
	})
}

func (c *tuiController) handleProviderUsage(message providerUsageMessage) (bool, error) {
	c.usageLoading = false
	if message.err == nil {
		c.model.providerUsage = ProviderUsage{
			Windows:           append([]UsageWindow(nil), message.usage.Windows...),
			MonthlyUsageUSD:   message.usage.MonthlyUsageUSD,
			LimitRemainingUSD: message.usage.LimitRemainingUSD,
		}
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleSubagentStatus(status subagent.Status) (bool, error) {
	c.model.subagentStatus = sanitizeSubagentStatus(status)
	c.dirty = true
	return false, nil
}

func sanitizeSubagentStatus(status subagent.Status) subagent.Status {
	sanitized := subagent.Status{
		Running:            max(0, status.Running),
		Active:             make([]subagent.JobStatus, 0, len(status.Active)),
		PendingCompletions: make([]subagent.Completion, 0, len(status.PendingCompletions)),
	}
	for _, job := range status.Active {
		job.ID = singleLine(job.ID, 120)
		job.Task = singleLine(job.Task, 120)
		sanitized.Active = append(sanitized.Active, job)
	}
	for _, completion := range status.PendingCompletions {
		completion.SubagentID = singleLine(completion.SubagentID, 120)
		completion.Task = singleLine(completion.Task, 120)
		sanitized.PendingCompletions = append(sanitized.PendingCompletions, completion)
	}
	return sanitized
}

func (c *tuiController) handleFileSearch(result filesearch.Result) (bool, error) {
	if !c.model.applyFileSearchResult(result) {
		return false, nil
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleSpinner() (bool, error) {
	if (!c.model.activity.animated() || c.model.permission.active()) && c.model.subagentStatus.Running == 0 {
		return false, nil
	}

	c.model.spinner++
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleUsageClock(ctx context.Context) (bool, error) {
	if c.model.running {
		c.requestUsage(ctx)
	}

	for _, window := range c.model.providerUsage.Windows {
		if !window.ResetsAt.IsZero() {
			c.dirty = true
			return false, nil
		}
	}
	return false, nil
}

func (c *tuiController) handleRender() (bool, error) {
	if err := renderIfDirty(c.renderer, c.model, c.output, &c.dirty, c.forceRedraw); err != nil {
		return false, err
	}
	c.forceRedraw = false
	return false, nil
}
