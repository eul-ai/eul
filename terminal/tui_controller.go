package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/term"

	"github.com/eul-ai/eul/agent"
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
	subagentStatus agent.SubagentStatus
	permission     PermissionRequest
	fileSearch     fileSearchResult
	err            error
}

type tuiController struct {
	model              *tuiModel
	renderer           *tuiRenderer
	engine             Engine
	output             io.Writer
	outputFD           int
	engineMessages     chan<- engineMessage
	stopped            <-chan struct{}
	fileSearch         *fileSearchRunner
	fileSearchMessages chan<- fileSearchResult
	usageRequests      chan<- struct{}
	setThinkingLevel   func(agent.ThinkingLevel) error
	saveCheckpoint     func(agent.Checkpoint, Checkpoint, bool) error
	listSessions       func(context.Context) ([]SessionSummary, []string, error)
	turnCancel         context.CancelFunc
	exitAfterTurn      error
	deferredSteering   []string
	permission         *PermissionRequest
	queuedPermissions  []PermissionRequest
	dirty              bool
	forceRedraw        bool
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
	if !c.model.running {
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		return true, parentErr
	}

	c.exitAfterTurn = parentErr
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
	action, err := reduceKeyWithFrame(c.model, key, c.renderer.frame)
	if err != nil {
		if !c.model.running {
			if checkpointErr := c.saveCurrentCheckpoint(false); checkpointErr != nil {
				return false, checkpointErr
			}
		}
		return false, err
	}

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
		if c.exitAfterTurn == nil {
			c.exitAfterTurn = io.EOF
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
	if c.exitAfterTurn != nil {
		if errors.Is(c.exitAfterTurn, io.EOF) {
			return true, nil
		}
		return true, c.exitAfterTurn
	}

	interrupted := c.model.interrupted
	if interrupted || message.err != nil {
		c.engine.ClearSteering()
		c.deferredSteering = nil
		c.model.restoreAllSteering()
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
	c.model.applyAgentEvent(*message.event)
	if c.model.permission.active() {
		c.model.activity = activity{kind: activityPermission}
	}
	if message.ack != nil {
		err := c.saveAgentCheckpoint(message.event.Checkpoint, true)
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
		c.model.providerUsage = agent.ProviderUsage{Windows: append([]agent.UsageWindow(nil), message.usage.Windows...)}
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleSubagentStatus(status agent.SubagentStatus) (bool, error) {
	c.model.subagentStatus = sanitizeSubagentStatus(status)
	c.dirty = true
	return false, nil
}

func sanitizeSubagentStatus(status agent.SubagentStatus) agent.SubagentStatus {
	sanitized := agent.SubagentStatus{
		Running:    max(0, status.Running),
		Finalizing: max(0, status.Finalizing),
		Completed:  max(0, status.Completed),
		Jobs:       make([]agent.SubagentJobStatus, 0, len(status.Jobs)),
	}
	for _, job := range status.Jobs {
		switch job.State {
		case agent.SubagentPending,
			agent.SubagentRunning,
			agent.SubagentFinalizing,
			agent.SubagentCanceling,
			agent.SubagentComplete,
			agent.SubagentFailed,
			agent.SubagentCanceled:
			job.ID = singleLine(job.ID, 120)
			job.Task = singleLine(job.Task, 120)
			job.Generations = max(0, job.Generations)
			job.GenerationLimit = max(0, job.GenerationLimit)
			sanitized.Jobs = append(sanitized.Jobs, job)
		}
	}
	return sanitized
}

func (c *tuiController) handlePermission(request PermissionRequest) (bool, error) {
	if request.Response == nil {
		return false, nil
	}
	if !c.model.running || c.model.interrupted || c.model.activity.kind == activityCanceling {
		respondPermission(request, false)
		return false, nil
	}
	if c.permission != nil {
		c.queuedPermissions = append(c.queuedPermissions, request)
		c.model.permission.total++
		c.dirty = true
		return false, nil
	}

	c.permission = &request
	c.model.showPermission(request, 1, 1)
	c.dirty = true
	return false, nil
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

func (c *tuiController) applyAction(ctx context.Context, action tuiAction) (bool, error) {
	switch action.kind {
	case tuiActionNone:
		return false, nil
	case tuiActionHelp:
		c.model.appendBlock(blockInfo, commandHelpText())
		if c.model.running {
			return false, nil
		}
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionOpenResume:
		if c.listSessions == nil {
			setInputError(c.model, errors.New("session resumption is unavailable"))
			return false, nil
		}
		summaries, warnings, err := c.listSessions(ctx)
		if err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		for _, warning := range warnings {
			c.model.appendBlock(blockInfo, warning)
		}
		c.model.openResumePicker(summaries)
	case tuiActionResume:
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		return false, &ResumeRequest{SessionID: action.text}
	case tuiActionNewSession:
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		return false, &NewSessionRequest{}
	case tuiActionCancel:
		c.interruptTurn()
	case tuiActionCompact:
		return false, c.startCompaction(ctx)
	case tuiActionExit:
		if !c.model.running {
			if err := c.saveCurrentCheckpoint(false); err != nil {
				return false, err
			}
		}
		return true, nil
	case tuiActionSubmit:
		return false, c.startTurn(ctx, action.prompt)
	case tuiActionSteer:
		if len(c.deferredSteering) > 0 || !c.engine.Steer(action.prompt) {
			c.deferredSteering = append(c.deferredSteering, action.prompt)
		}
		c.model.queueSteering(action.prompt)
	case tuiActionShowGoal:
		goal, ok := c.engine.Goal()
		switch {
		case !ok:
			c.model.appendBlock(blockInfo, "No goal is set")
		case goal.Complete:
			c.model.appendBlock(blockInfo, "Goal complete: "+goal.Objective)
		default:
			c.model.appendBlock(blockInfo, "Goal: "+goal.Objective)
		}
		if c.model.running {
			return false, nil
		}
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionSetGoal:
		if err := c.engine.SetGoal(action.prompt); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		if c.model.running {
			c.model.appendBlock(blockInfo, "Goal set: "+action.prompt)
			return false, nil
		}
		return false, c.startTurn(ctx, action.prompt)
	case tuiActionClearGoal:
		_, hadGoal := c.engine.Goal()
		c.engine.ClearGoal()
		if hadGoal {
			c.model.appendBlock(blockInfo, "Goal cleared")
		} else {
			c.model.appendBlock(blockInfo, "No goal is set")
		}
		if !c.model.running {
			return false, c.saveCurrentCheckpoint(false)
		}
	case tuiActionDequeue:
		c.restoreQueuedInput()
	case tuiActionSetThinking:
		if c.setThinkingLevel == nil {
			setInputError(c.model, errors.New("thinking level selection is unavailable"))
			return false, nil
		}
		if err := c.setThinkingLevel(action.thinkingLevel); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.thinkingLevel = action.thinkingLevel
		if c.model.running {
			return false, nil
		}
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionAllowPermission:
		c.resolvePermission(true)
	case tuiActionDenyPermission:
		c.resolvePermission(false)
	case tuiActionCopy:
		encoded := base64.StdEncoding.EncodeToString([]byte(action.text))
		if err := writeOutput(c.output, "\x1b]52;c;%s\x07", encoded); err != nil {
			return false, err
		}
	case tuiActionRedraw:
		c.forceRedraw = true
	}
	return false, nil
}

func (c *tuiController) startTurn(ctx context.Context, prompt string) error {
	c.model.beginTurn(prompt)
	if err := c.saveCurrentCheckpoint(true); err != nil {
		return err
	}

	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineTurn(turnContext, c.engine, prompt, c.engineMessages, c.stopped)
	return nil
}

func (c *tuiController) startCompaction(ctx context.Context) error {
	c.model.beginCompaction()
	if err := c.saveCurrentCheckpoint(true); err != nil {
		return err
	}

	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineCompaction(turnContext, c.engine, c.engineMessages, c.stopped)
	return nil
}

func (c *tuiController) startDeferredTurn(ctx context.Context) error {
	if len(c.deferredSteering) == 0 {
		return nil
	}
	prompt := c.deferredSteering[0]
	c.deferredSteering = c.deferredSteering[1:]
	c.model.removeSteering([]string{prompt})
	return c.startTurn(ctx, prompt)
}

func (c *tuiController) restoreQueuedInput() {
	messages := c.engine.ClearSteering()
	messages = append(messages, c.deferredSteering...)
	c.deferredSteering = nil
	c.model.restoreSteering(messages)
}

func (c *tuiController) interruptTurn() {
	c.model.interrupted = true
	c.cancelTurn()
}

func (c *tuiController) resolvePermission(allowed bool) {
	if c.permission == nil {
		return
	}

	respondPermission(*c.permission, allowed)
	c.permission = nil
	if len(c.queuedPermissions) == 0 {
		c.model.clearPermission()
		c.restoreActivityAfterPermission()
		return
	}

	next := c.queuedPermissions[0]
	c.queuedPermissions = c.queuedPermissions[1:]
	index := c.model.permission.index + 1
	total := c.model.permission.total
	c.permission = &next
	c.model.showPermission(next, index, total)
}

func (c *tuiController) denyPermissions() {
	if c.permission != nil {
		respondPermission(*c.permission, false)
	}
	for _, request := range c.queuedPermissions {
		respondPermission(request, false)
	}
	c.permission = nil
	c.queuedPermissions = nil
	c.model.clearPermission()
}

func (c *tuiController) restoreActivityAfterPermission() {
	if detail, ok := c.model.pendingToolActivity(); ok {
		c.model.activity = activity{kind: activityTool, detail: detail}
		return
	}
	c.model.activity = activity{kind: activityThinking}
}

func respondPermission(request PermissionRequest, allowed bool) {
	select {
	case request.Response <- allowed:
	default:
	}
}

func (c *tuiController) cancelTurn() {
	c.denyPermissions()
	c.restoreQueuedInput()
	if c.turnCancel != nil {
		c.turnCancel()
	}
	c.model.activity = activity{kind: activityCanceling}
}

func (c *tuiController) saveCurrentCheckpoint(active bool) error {
	if c.saveCheckpoint == nil {
		return nil
	}
	engine, ok := c.engine.(checkpointEngine)
	if !ok {
		return errCheckpointUnavailable
	}
	checkpoint, err := engine.Checkpoint()
	if err != nil {
		return err
	}
	return c.saveAgentCheckpoint(&checkpoint, active)
}

func (c *tuiController) saveAgentCheckpoint(checkpoint *agent.Checkpoint, active bool) error {
	if c.saveCheckpoint == nil {
		return nil
	}
	if checkpoint == nil {
		return errors.New("terminal: agent checkpoint is missing")
	}
	return c.saveCheckpoint(*checkpoint, checkpointModel(c.model), active)
}
