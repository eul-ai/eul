package terminal

import (
	"context"
	"fmt"
	"io"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal/clipboard"
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
	key            keyEvent
	engine         engineMessage
	providerUsage  providerUsageMessage
	subagentStatus subagent.Status
	permission     PermissionRequest
	image          agent.Image
	requestID      uint64
	fileSearch     fileSearchResult
	err            error
}

type controllerOptions struct {
	model              *tuiModel
	output             io.Writer
	outputFD           int
	engineMessages     chan<- engineMessage
	stopped            <-chan struct{}
	fileSearch         *fileSearchRunner
	fileSearchMessages chan<- fileSearchResult
	usageRequests      chan<- struct{}
	clipboardImages    chan<- tuiEvent
	operations         Operations
	controls           Controls
	stateChanges       StateChanges
	sessions           Sessions
	readClipboardImage func(context.Context) (agent.Image, error)
}

func newTUIController(config controllerOptions) *tuiController {
	readClipboardImage := config.readClipboardImage
	if readClipboardImage == nil {
		readClipboardImage = clipboard.ReadImage
	}
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
		fileSearchMessages: config.fileSearchMessages,
		usageRequests:      config.usageRequests,
		stateChanges:       config.stateChanges,
		sessions:           config.sessions,
		readClipboardImage: readClipboardImage,
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
	fileSearch                   *fileSearchRunner
	fileSearchMessages           chan<- fileSearchResult
	usageRequests                chan<- struct{}
	stateChanges                 StateChanges
	sessions                     Sessions
	readClipboardImage           func(context.Context) (agent.Image, error)
	clipboardImages              chan<- tuiEvent
	clipboardRequests            map[uint64]context.CancelFunc
	nextClipboardID              uint64
	turnCancel                   context.CancelFunc
	exitAfterTurn                bool
	exitAfterTurnErr             error
	outcome                      RunOutcome
	permission                   *PermissionRequest
	queuedPermissions            []PermissionRequest
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
		return c.handleUsageClock()
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

func (c *tuiController) handleKey(ctx context.Context, key keyEvent) (bool, error) {
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
	if c.fileSearch != nil {
		c.fileSearch.update(ctx, c.model.takeFileSearchCommand(), c.fileSearchMessages)
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

func (c *tuiController) handleEngineMessage(ctx context.Context, message engineMessage) (bool, error) {
	if message.snapshot != nil {
		message.snapshot <- checkpointModel(c.model, c.model.pendingSteering())
		return false, nil
	}
	if !message.done {
		return c.handleAgentEvent(message)
	}

	c.denyPermissions()
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
	requestProviderUsage(c.usageRequests)
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
		if c.model.deliverSteering(message.event.Text) {
			c.model.appendBlock(blockUser, message.event.Text)
			c.model.setActiveActivity(activity{kind: activityThinking})
		}
	} else {
		c.model.applyAgentEvent(*message.event)
	}
	if c.model.permission.active() {
		c.model.activity = activity{kind: activityPermission}
	}
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleProviderUsage(message providerUsageMessage) (bool, error) {
	if message.err == nil {
		c.model.providerUsage = ProviderUsage{Windows: append([]UsageWindow(nil), message.usage.Windows...)}
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
		switch job.State {
		case subagent.StateRunning, subagent.StateCanceling:
			job.ID = singleLine(job.ID, 120)
			job.Task = singleLine(job.Task, 120)
			sanitized.Active = append(sanitized.Active, job)
		}
	}
	for _, completion := range status.PendingCompletions {
		switch completion.Status {
		case subagent.StateComplete, subagent.StateFailed, subagent.StateCanceled, subagent.StateInterrupted:
			completion.SubagentID = singleLine(completion.SubagentID, 120)
			completion.Task = singleLine(completion.Task, 120)
			sanitized.PendingCompletions = append(sanitized.PendingCompletions, completion)
		}
	}
	return sanitized
}

func (c *tuiController) handleFileSearch(result fileSearchResult) (bool, error) {
	if !c.model.applyFileSearchResult(result) {
		return false, nil
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleSpinner() (bool, error) {
	if (c.model.activity.kind == activityReady || c.model.activity.kind == activityPermission || c.model.activity.kind == activityError) && c.model.subagentStatus.Running == 0 {
		return false, nil
	}

	c.model.spinner++
	c.dirty = true
	return false, nil
}

func (c *tuiController) handleUsageClock() (bool, error) {
	for _, window := range c.model.providerUsage.Windows {
		if !window.ResetsAt.IsZero() {
			c.dirty = true
			return false, nil
		}
	}
	return false, nil
}

func (c *tuiController) handleRender() (bool, error) {
	normalizeViewport(c.model, c.renderer)
	if err := renderIfDirty(c.renderer, c.model, c.output, &c.dirty, c.forceRedraw); err != nil {
		return false, err
	}
	c.forceRedraw = false
	return false, nil
}
