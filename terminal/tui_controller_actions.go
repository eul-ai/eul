package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"

	"github.com/eul-ai/eul/agent"
)

func (c *tuiController) applyAction(ctx context.Context, action tuiAction) (bool, error) {
	exit, err := c.runAction(ctx, action)
	if err != nil || c.model.running {
		return exit, err
	}
	switch action.kind {
	case tuiActionCommand, tuiActionExit, tuiActionResume, tuiActionSetThinking:
		return exit, c.saveCurrentCheckpoint(false)
	}
	return exit, nil
}

func (c *tuiController) runAction(ctx context.Context, action tuiAction) (bool, error) {
	switch action.kind {
	case tuiActionCommand:
		return action.command.run(c, ctx, action.text)
	case tuiActionExit:
		return c.exit(ctx, "")
	case tuiActionResume:
		c.cancelClipboardRequests()
		c.outcome = RunOutcome{Action: RunResumeSession, SessionID: action.text}
		return true, nil
	case tuiActionCancel:
		c.interruptTurn()
	case tuiActionSubmit:
		if err := c.startTurn(ctx, action.content); err != nil {
			return false, err
		}
		c.cancelClipboardRequests()
		c.model.finishSubmission(action.content)
	case tuiActionSteer:
		accepted := false
		if len(c.model.steering.deferred) == 0 && c.controls.Steer != nil {
			accepted = c.controls.Steer(action.content)
		}
		c.model.enqueueSteering(action.content, accepted)
	case tuiActionDequeue:
		c.restoreQueuedInput()
	case tuiActionSetThinking:
		if err := c.controls.SetThinkingLevel(action.thinkingLevel); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		c.model.thinkingLevel = action.thinkingLevel
	case tuiActionAttachImage:
		if c.model.imageCount()+len(c.clipboardRequests) >= maxAttachedImages {
			setInputError(c.model, errTooManyImages)
			return false, nil
		}
		c.nextClipboardID++
		requestID := c.nextClipboardID
		readContext, cancel := context.WithCancel(ctx)
		c.clipboardRequests[requestID] = cancel
		c.model.reserveImage(requestID)
		go loadClipboardImage(readContext, requestID, c.readClipboardImage, c.clipboardImages, c.stopped)
	case tuiActionResolvePermission:
		c.resolvePermission(action.permissionDecision)
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

func (c *tuiController) startTurn(ctx context.Context, content []agent.ContentPart) error {
	if c.operations.RunTurn == nil {
		return errors.New("agent turns are unavailable")
	}
	c.model.beginTurnOperation()
	if err := c.saveCurrentCheckpoint(true); err != nil {
		c.model.rollbackTurnStart()
		return err
	}
	c.model.appendUserContent(content)

	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineTurn(turnContext, c.operations.RunTurn, content, c.engineMessages, c.stopped)
	return nil
}

func (c *tuiController) startCompaction(ctx context.Context) error {
	if c.operations.Compact == nil {
		return errors.New("compaction is unavailable")
	}
	c.model.beginCompaction()
	if err := c.saveCurrentCheckpoint(true); err != nil {
		return err
	}

	turnContext, cancel := context.WithCancel(ctx)
	c.turnCancel = cancel
	go runEngineCompaction(turnContext, c.operations.Compact, c.engineMessages, c.stopped)
	return nil
}

func (c *tuiController) startDeferredTurn(ctx context.Context) error {
	content, ok := c.model.nextDeferredSteering()
	if !ok {
		return nil
	}
	if err := c.startTurn(ctx, content); err != nil {
		c.model.restoreDeferredSteering(content)
		return err
	}
	return nil
}

func (c *tuiController) restoreQueuedInput() {
	c.model.restoreSteering(c.model.clearSteering(c.controls.ClearSteering))
}

func (c *tuiController) interruptTurn() {
	c.model.interrupted = true
	c.cancelTurn()
}

func (c *tuiController) cancelTurn() {
	c.respondPermissions(PermissionDenyOnce)
	c.restoreQueuedInput()
	if c.turnCancel != nil {
		c.turnCancel()
	}
	c.model.activity = activity{kind: activityCanceling}
}

func (c *tuiController) saveCurrentCheckpoint(active bool) error {
	if c.stateChanges.Notify == nil {
		return nil
	}
	return c.stateChanges.Notify(slices.Clone(c.model.transcript.Entries), c.model.draft(), active)
}
