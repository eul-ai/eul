package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/term"

	"github.com/eul-ai/eul/agent"
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
	subagentStatus SubagentStatus
	permission     PermissionRequest
	image          agent.Image
	requestID      uint64
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
	setFastMode        func(bool) error
	saveCheckpoint     func(agent.Checkpoint, Checkpoint, bool) error
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
		c.model.restoreSteering(c.steering.restore(c.engine))
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
		c.model.providerUsage = ProviderUsage{Windows: append([]UsageWindow(nil), message.usage.Windows...)}
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) handleSubagentStatus(status SubagentStatus) (bool, error) {
	c.model.subagentStatus = sanitizeSubagentStatus(status)
	c.dirty = true
	return false, nil
}

func sanitizeSubagentStatus(status SubagentStatus) SubagentStatus {
	sanitized := SubagentStatus{
		Running:    max(0, status.Running),
		Finalizing: max(0, status.Finalizing),
		Active:     make([]SubagentJobStatus, 0, len(status.Active)),
		Awaiting:   make([]SubagentCompletionStatus, 0, len(status.Awaiting)),
	}
	for _, job := range status.Active {
		switch job.State {
		case SubagentRunning, SubagentFinalizing, SubagentCanceling:
			job.ID = singleLine(job.ID, 120)
			job.Task = singleLine(job.Task, 120)
			job.Generations = max(0, job.Generations)
			job.GenerationLimit = max(0, job.GenerationLimit)
			sanitized.Active = append(sanitized.Active, job)
		}
	}
	for _, completion := range status.Awaiting {
		switch completion.State {
		case SubagentComplete, SubagentFailed, SubagentCanceled, SubagentInterrupted:
			completion.SubagentID = singleLine(completion.SubagentID, 120)
			completion.Task = singleLine(completion.Task, 120)
			sanitized.Awaiting = append(sanitized.Awaiting, completion)
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

func (c *tuiController) handleClipboardImage(requestID uint64, image agent.Image, err error) (bool, error) {
	cancel, active := c.clipboardRequests[requestID]
	if !active {
		return false, nil
	}
	if c.model.running {
		cancel()
		delete(c.clipboardRequests, requestID)
		c.model.removePendingImage(requestID)
		return false, nil
	}
	cancel()
	delete(c.clipboardRequests, requestID)

	if err == nil {
		err = clipboard.ValidateImage(image)
	}
	if err != nil {
		if c.model.removePendingImage(requestID) {
			setInputError(c.model, err)
			c.dirty = true
		}
		return false, nil
	}
	if err := c.model.resolveImage(requestID, image); err != nil {
		c.model.removePendingImage(requestID)
		setInputError(c.model, err)
	}
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
		c.model.appendBlock(blockInfo, commandHelpText(c.model.fastModeAvailable))
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
		c.cancelClipboardRequests()
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		c.outcome = RunOutcome{Action: RunResumeSession, SessionID: action.text}
		return true, nil
	case tuiActionNewSession:
		c.cancelClipboardRequests()
		if err := c.saveCurrentCheckpoint(false); err != nil {
			return false, err
		}
		c.outcome = RunOutcome{Action: RunNewSession}
		return true, nil
	case tuiActionCancel:
		c.interruptTurn()
	case tuiActionCompact:
		return false, c.startCompaction(ctx)
	case tuiActionToggleFast:
		if !c.model.fastModeAvailable || c.setFastMode == nil {
			setInputError(c.model, errors.New("fast mode is unavailable for this model"))
			return false, nil
		}
		if err := c.setFastMode(!c.model.fastMode); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.fastMode = !c.model.fastMode
		state := "off"
		if c.model.fastMode {
			state = "on"
		}
		c.model.appendBlock(blockInfo, "Fast mode "+state)
		if c.model.running {
			return false, nil
		}
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionExit:
		c.cancelClipboardRequests()
		if !c.model.running {
			if err := c.saveCurrentCheckpoint(false); err != nil {
				return false, err
			}
		}
		return true, nil
	case tuiActionSubmit:
		if err := c.startTurn(ctx, action.content); err != nil {
			return false, err
		}
		c.cancelClipboardRequests()
		c.model.finishSubmission(action.content)
	case tuiActionSteer:
		c.steering.enqueue(c.engine, action.prompt)
		c.model.conversationVersion++
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
		return false, c.startTurn(ctx, []agent.ContentPart{{Kind: agent.ContentPartText, Text: action.prompt}})
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
	case tuiActionAttachImage:
		if c.readClipboardImage == nil || c.clipboardImages == nil {
			setInputError(c.model, errors.New("clipboard images are unavailable"))
			return false, nil
		}
		if c.model.imageCount()+len(c.clipboardRequests) >= maxAttachedImages {
			setInputError(c.model, errTooManyImages)
			return false, nil
		}
		c.nextClipboardID++
		requestID := c.nextClipboardID
		readContext, cancel := context.WithCancel(ctx)
		if c.clipboardRequests == nil {
			c.clipboardRequests = make(map[uint64]context.CancelFunc)
		}
		c.clipboardRequests[requestID] = cancel
		c.model.reserveImage(requestID)
		go loadClipboardImage(readContext, requestID, c.readClipboardImage, c.clipboardImages, c.stopped)
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

func loadClipboardImage(
	ctx context.Context,
	requestID uint64,
	read func(context.Context) (agent.Image, error),
	events chan<- tuiEvent,
	stopped <-chan struct{},
) {
	image, err := read(ctx)
	event := tuiEvent{kind: tuiEventClipboardImage, requestID: requestID, image: image, err: err}
	select {
	case events <- event:
	case <-ctx.Done():
	case <-stopped:
	}
}

func (c *tuiController) startTurn(ctx context.Context, content []agent.ContentPart) error {
	c.model.beginTurnOperation()
	if err := c.saveCurrentCheckpoint(true); err != nil {
		c.model.rollbackTurnStart()
		return err
	}
	c.model.appendUserContent(content)

	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineTurn(turnContext, c.engine, content, c.engineMessages, c.stopped)
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
	prompt, ok := c.steering.nextDeferred()
	if !ok {
		return nil
	}
	c.model.conversationVersion++
	if err := c.startTurn(ctx, []agent.ContentPart{{Kind: agent.ContentPartText, Text: prompt}}); err != nil {
		c.steering.restoreDeferred(prompt)
		c.model.conversationVersion++
		return err
	}
	return nil
}

func (c *tuiController) restoreQueuedInput() {
	messages := c.steering.restore(c.engine)
	c.model.restoreSteering(messages)
	if len(messages) > 0 {
		c.model.conversationVersion++
	}
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

func (c *tuiController) cancelRemovedClipboardRequests(previous []uint64) {
	remaining := make(map[uint64]struct{}, len(c.model.pendingImageRequests()))
	for _, requestID := range c.model.pendingImageRequests() {
		remaining[requestID] = struct{}{}
	}
	for _, requestID := range previous {
		if _, ok := remaining[requestID]; !ok {
			c.cancelClipboardRequest(requestID)
		}
	}
}

func (c *tuiController) cancelClipboardRequest(requestID uint64) {
	cancel, ok := c.clipboardRequests[requestID]
	if !ok {
		return
	}
	cancel()
	delete(c.clipboardRequests, requestID)
}

func (c *tuiController) cancelClipboardRequests() {
	for requestID, cancel := range c.clipboardRequests {
		cancel()
		delete(c.clipboardRequests, requestID)
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
	return c.saveCheckpoint(*checkpoint, checkpointModel(c.model, c.steering.pending()), active)
}
