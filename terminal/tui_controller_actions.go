package terminal

import (
	"context"
	"encoding/base64"

	"github.com/eul-ai/eul/agent"
)

func (c *tuiController) applyAction(ctx context.Context, action tuiAction) (bool, error) {
	exit, err := c.runAction(ctx, action)
	if err != nil || c.model.running {
		return exit, err
	}
	switch action.kind {
	case tuiActionCommand, tuiActionExit, tuiActionResume, tuiActionSetThinking:
		return exit, c.saveDraft()
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
		if err := c.startTurn(action.content); err != nil {
			return false, err
		}
		c.cancelClipboardRequests()
		c.model.finishSubmission(action.content)
	case tuiActionSteer:
		if err := c.model.session.Send(action.content); err != nil {
			return false, err
		}
		c.model.syncConversation()
		c.model.syncQueue()
	case tuiActionDequeue:
		c.model.restoreQueued(c.model.session.TakeQueued())
	case tuiActionSetThinking:
		if err := c.model.session.SetThinkingLevel(action.thinkingLevel); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
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
		c.model.session.ResolvePermission(action.permissionDecision)
		c.model.syncPermissions()
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

func (c *tuiController) startTurn(content []agent.ContentPart) error {
	if err := c.model.session.Send(content); err != nil {
		return err
	}
	c.model.beginTurn(activityThinking)
	return nil
}

func (c *tuiController) startCompaction() error {
	if err := c.model.session.Compact(); err != nil {
		return err
	}
	c.model.beginTurn(activityCompacting)
	return nil
}

func (c *tuiController) interruptTurn() {
	c.model.interrupted = true
	c.cancelTurn()
}

func (c *tuiController) cancelTurn() {
	c.model.restoreQueued(c.model.session.Interrupt())
	c.model.syncPermissions()
	c.model.activity = activity{kind: activityCanceling}
}

func (c *tuiController) saveDraft() error {
	return c.model.session.SaveDraft(c.model.draft())
}
