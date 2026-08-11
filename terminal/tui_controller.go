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
	readClipboardImage func(context.Context) (agent.Image, error)
	turnCancel         context.CancelFunc
	exitAfterTurn      error
	deferredSteering   []string
	dirty              bool
	forceRedraw        bool
}

func (c *tuiController) transition(ctx context.Context, event tuiEvent) (bool, error) {
	switch event.kind {
	case tuiEventParentCanceled:
		if !c.model.running {
			if err := c.saveCurrentCheckpoint(false); err != nil {
				return false, err
			}
			return true, event.err
		}
		c.exitAfterTurn = event.err
		c.cancelTurn()
	case tuiEventInterrupt:
		action, err := reduceInterrupt(c.model)
		if err != nil {
			if checkpointErr := c.saveCurrentCheckpoint(false); checkpointErr != nil {
				return false, checkpointErr
			}
			return false, err
		}
		if action.kind == tuiActionCancel {
			c.cancelTurn()
		}
	case tuiEventResize:
		width, height, err := term.GetSize(c.outputFD)
		if err != nil {
			return false, fmt.Errorf("terminal: get size: %w", err)
		}
		c.model.width = width
		c.model.height = height
		c.model.selection = textSelection{}
	case tuiEventKey:
		action, err := reduceKeyWithFrame(c.model, event.key, c.renderer.frame)
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
	case tuiEventEngine:
		message := event.engine
		if !message.done {
			c.model.applyAgentEvent(*message.event)
			if message.ack != nil {
				err := c.saveAgentCheckpoint(message.event.Checkpoint, true)
				message.ack <- err
				if err != nil {
					return false, err
				}
			}
			break
		}
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
	case tuiEventProviderUsage:
		if event.providerUsage.err == nil {
			c.model.providerUsage = agent.ProviderUsage{Windows: append([]agent.UsageWindow(nil), event.providerUsage.usage.Windows...)}
		}
	case tuiEventSubagentStatus:
		status := agent.SubagentStatus{
			Running:    max(0, event.subagentStatus.Running),
			Finalizing: max(0, event.subagentStatus.Finalizing),
			Completed:  max(0, event.subagentStatus.Completed),
			Jobs:       make([]agent.SubagentJobStatus, 0, len(event.subagentStatus.Jobs)),
		}
		for _, job := range event.subagentStatus.Jobs {
			switch job.State {
			case agent.SubagentRunning, agent.SubagentFinalizing, agent.SubagentCanceling:
				job.ID = singleLine(job.ID, 120)
				job.Task = singleLine(job.Task, 120)
				job.Generations = max(0, job.Generations)
				job.GenerationLimit = max(0, job.GenerationLimit)
				status.Jobs = append(status.Jobs, job)
			}
		}
		c.model.subagentStatus = status
	case tuiEventFileSearch:
		if !c.model.applyFileSearchResult(event.fileSearch) {
			return false, nil
		}
	case tuiEventSpinner:
		if (c.model.activity.kind == activityReady || c.model.activity.kind == activityError) && len(c.model.subagentStatus.Jobs) == 0 {
			return false, nil
		}
		c.model.spinner++
	case tuiEventUsageClock:
		redraw := false
		for _, window := range c.model.providerUsage.Windows {
			if !window.ResetsAt.IsZero() {
				redraw = true
				break
			}
		}
		if !redraw {
			return false, nil
		}
	case tuiEventRender:
		c.renderer.normalizeViewport(c.model)
		if err := renderIfDirty(c.renderer, c.model, c.output, &c.dirty, c.forceRedraw); err != nil {
			return false, err
		}
		c.forceRedraw = false
		return false, nil
	}

	c.dirty = true
	return false, nil
}

func (c *tuiController) applyAction(ctx context.Context, action tuiAction) (bool, error) {
	switch action.kind {
	case tuiActionNone:
		return false, nil
	case tuiActionHelp:
		c.model.appendBlock(blockInfo, commandHelpText())
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
		c.cancelTurn()
	case tuiActionCompact:
		c.model.beginCompaction()
		if err := c.saveCurrentCheckpoint(true); err != nil {
			return false, err
		}
		c.startCompaction(ctx)
	case tuiActionExit:
		if !c.model.running {
			if err := c.saveCurrentCheckpoint(false); err != nil {
				return false, err
			}
		}
		return true, nil
	case tuiActionSubmit:
		if err := c.saveCurrentCheckpoint(true); err != nil {
			return false, err
		}
		c.startTurn(ctx, action.prompt, action.images)
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
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionSetGoal:
		if err := c.engine.SetGoal(action.prompt); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.beginTurn(action.prompt)
		if err := c.saveCurrentCheckpoint(true); err != nil {
			return false, err
		}
		c.startTurn(ctx, action.prompt, nil)
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
		return false, c.saveCurrentCheckpoint(false)
	case tuiActionAttachImage:
		if c.readClipboardImage == nil {
			setInputError(c.model, errors.New("clipboard images are unavailable"))
			return false, nil
		}
		image, err := c.readClipboardImage(ctx)
		if err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.attachImage(image)
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

func (c *tuiController) startTurn(ctx context.Context, prompt string, images []agent.Image) {
	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineTurn(turnContext, c.engine, prompt, images, c.engineMessages, c.stopped)
}

func (c *tuiController) startCompaction(ctx context.Context) {
	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineCompaction(turnContext, c.engine, c.engineMessages, c.stopped)
}

func (c *tuiController) startDeferredTurn(ctx context.Context) error {
	if len(c.deferredSteering) == 0 {
		return nil
	}
	prompt := c.deferredSteering[0]
	c.deferredSteering = c.deferredSteering[1:]
	c.model.removeSteering([]string{prompt})
	c.model.beginTurn(prompt)
	if err := c.saveCurrentCheckpoint(true); err != nil {
		return err
	}
	c.startTurn(ctx, prompt, nil)
	return nil
}

func (c *tuiController) restoreQueuedInput() {
	messages := c.engine.ClearSteering()
	messages = append(messages, c.deferredSteering...)
	c.deferredSteering = nil
	c.model.restoreSteering(messages)
}

func (c *tuiController) cancelTurn() {
	c.restoreQueuedInput()
	if c.turnCancel != nil {
		c.turnCancel()
	}
	c.model.activity = activity{kind: activityCanceling}
}

type checkpointEngine interface {
	Checkpoint() (agent.Checkpoint, error)
}

func (c *tuiController) saveCurrentCheckpoint(active bool) error {
	if c.saveCheckpoint == nil {
		return nil
	}
	engine, ok := c.engine.(checkpointEngine)
	if !ok {
		return errors.New("terminal: engine checkpointing is unavailable")
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
