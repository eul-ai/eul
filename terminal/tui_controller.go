package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/term"

	"yaah/agent"
)

type tuiEventKind uint8

const (
	tuiEventParentCanceled tuiEventKind = iota
	tuiEventInterrupt
	tuiEventResize
	tuiEventKey
	tuiEventEngine
	tuiEventProviderUsage
	tuiEventFileSearch
	tuiEventSpinner
	tuiEventUsageClock
	tuiEventRender
)

type tuiEvent struct {
	kind          tuiEventKind
	key           keyEvent
	engine        engineMessage
	providerUsage providerUsageMessage
	fileSearch    fileSearchResult
	err           error
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
	turnCancel         context.CancelFunc
	exitAfterTurn      error
	dirty              bool
	forceRedraw        bool
}

func (c *tuiController) transition(ctx context.Context, event tuiEvent) (bool, error) {
	switch event.kind {
	case tuiEventParentCanceled:
		if !c.model.running {
			return true, event.err
		}
		c.exitAfterTurn = event.err
		c.cancelTurn()
	case tuiEventInterrupt:
		action, err := reduceInterrupt(c.model)
		if err != nil {
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
		c.model.finishTurn(message.err)
		requestProviderUsage(c.usageRequests)
	case tuiEventProviderUsage:
		if event.providerUsage.err == nil {
			c.model.providerUsage = agent.ProviderUsage{Windows: append([]agent.UsageWindow(nil), event.providerUsage.usage.Windows...)}
		}
	case tuiEventFileSearch:
		if !c.model.applyFileSearchResult(event.fileSearch) {
			return false, nil
		}
	case tuiEventSpinner:
		if c.model.activity.kind == activityReady || c.model.activity.kind == activityError {
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
	case tuiActionCancel:
		c.cancelTurn()
	case tuiActionReset:
		if err := c.engine.Reset(); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.clearConversation()
	case tuiActionExit:
		return true, nil
	case tuiActionSubmit:
		turnContext, cancel := context.WithCancel(ctx)
		c.turnCancel = cancel
		go runEngineTurn(turnContext, c.engine, action.prompt, c.engineMessages, c.stopped)
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

func (c *tuiController) cancelTurn() {
	if c.turnCancel != nil {
		c.turnCancel()
	}
	c.model.activity = activity{kind: activityCanceling}
}
