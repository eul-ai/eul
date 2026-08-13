package terminal

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/term"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
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

type controllerEngine interface {
	RunContent(context.Context, []agent.ContentPart, agent.EventSink) (agent.RunResult, error)
	Compact(context.Context, agent.EventSink) error
	Steer(string) bool
	ClearSteering() []string
	SetGoal(string) error
	Goal() (agent.GoalState, bool)
	ClearGoal()
}

type tuiController struct {
	model              *tuiModel
	renderer           *tuiRenderer
	engine             controllerEngine
	operations         Operations
	controls           Controls
	output             io.Writer
	outputFD           int
	engineMessages     chan<- engineMessage
	stopped            <-chan struct{}
	fileSearch         *fileSearchRunner
	fileSearchMessages chan<- fileSearchResult
	usageRequests      chan<- struct{}
	setThinkingLevel   func(agent.ThinkingLevel) error
	setFastMode        func(bool) error
	saveCheckpoint     func(Checkpoint, bool) error
	listSessions       func(context.Context) ([]SessionSummary, []string, error)
	readClipboardImage func(context.Context) (agent.Image, error)
	clipboardImages    chan<- tuiEvent
	clipboardRequests  map[uint64]context.CancelFunc
	nextClipboardID    uint64
	turnCancel         context.CancelFunc
	exitAfterTurn      bool
	exitAfterTurnErr   error
	outcome            RunOutcome
	steering           steeringCoordinator
	permission         *PermissionRequest
	queuedPermissions  []PermissionRequest
	dirty              bool
	forceRedraw        bool
}

func (c *tuiController) transition(ctx context.Context, event tuiEvent) (bool, error) {
	c.prepareCallbacks()
	c.model.steeringView = &c.steering

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

func (c *tuiController) prepareCallbacks() {
	if c.engine == nil {
		return
	}
	if c.operations.RunTurn == nil {
		c.operations.RunTurn = func(ctx context.Context, content []agent.ContentPart, sink agent.EventSink) error {
			_, err := c.engine.RunContent(ctx, content, sink)
			return err
		}
	}
	if c.operations.Compact == nil {
		c.operations.Compact = c.engine.Compact
	}
	if c.controls.Steer == nil {
		c.controls.Steer = c.engine.Steer
	}
	if c.controls.ClearSteering == nil {
		c.controls.ClearSteering = c.engine.ClearSteering
	}
	if c.controls.SetGoal == nil {
		c.controls.SetGoal = c.engine.SetGoal
	}
	if c.controls.Goal == nil {
		c.controls.Goal = c.engine.Goal
	}
	if c.controls.ClearGoal == nil {
		c.controls.ClearGoal = c.engine.ClearGoal
	}
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
	width, height, err := term.GetSize(c.outputFD)
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
		c.model.restoreSteering(c.steering.restore(c.controls.ClearSteering))
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
		if c.steering.delivered(message.event.Text) {
			c.model.appendBlock(blockUser, message.event.Text)
			c.model.setActiveActivity(activity{kind: activityThinking})
		}
	} else {
		c.model.applyAgentEvent(*message.event)
	}
	if c.model.permission.active() {
		c.model.activity = activity{kind: activityPermission}
	}
	if message.ack != nil {
		err := c.saveCurrentCheckpoint(true)
		message.ack <- err
		if err != nil {
			return false, err
		}
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
		Finalizing:         max(0, status.Finalizing),
		Active:             make([]subagent.JobStatus, 0, len(status.Active)),
		PendingCompletions: make([]subagent.Completion, 0, len(status.PendingCompletions)),
	}
	for _, job := range status.Active {
		switch job.State {
		case subagent.StateRunning, subagent.StateFinalizing, subagent.StateCanceling:
			job.ID = singleLine(job.ID, 120)
			job.Task = singleLine(job.Task, 120)
			job.Generations = max(0, job.Generations)
			job.GenerationLimit = max(0, job.GenerationLimit)
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
	if (c.model.activity.kind == activityReady || c.model.activity.kind == activityPermission || c.model.activity.kind == activityError) && c.model.subagentStatus.Running == 0 && c.model.subagentStatus.Finalizing == 0 {
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
	if err := renderIfDirty(c.renderer, c.model, c.output, &c.dirty, c.forceRedraw); err != nil {
		return false, err
	}
	c.forceRedraw = false
	return false, nil
}
