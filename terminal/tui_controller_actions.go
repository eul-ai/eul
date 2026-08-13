package terminal

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/eul-ai/eul/agent"
)

func (c *tuiController) applyAction(ctx context.Context, action tuiAction) (bool, error) {
	c.prepareCallbacks()
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
		c.steering.enqueue(c.controls.Steer, action.prompt)
		c.model.conversationVersion++
	case tuiActionShowGoal:
		if c.controls.Goal == nil {
			setInputError(c.model, errors.New("goal inspection is unavailable"))
			return false, nil
		}
		goal, ok := c.controls.Goal()
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
		if c.controls.SetGoal == nil {
			setInputError(c.model, errors.New("goal updates are unavailable"))
			return false, nil
		}
		if err := c.controls.SetGoal(action.prompt); err != nil {
			setInputError(c.model, err)
			return false, nil
		}
		if c.model.running {
			c.model.appendBlock(blockInfo, "Goal set: "+action.prompt)
			return false, nil
		}
		return false, c.startTurn(ctx, []agent.ContentPart{{Kind: agent.ContentPartText, Text: action.prompt}})
	case tuiActionClearGoal:
		if c.controls.Goal == nil || c.controls.ClearGoal == nil {
			setInputError(c.model, errors.New("goal updates are unavailable"))
			return false, nil
		}
		_, hadGoal := c.controls.Goal()
		c.controls.ClearGoal()
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

func (c *tuiController) startTurn(ctx context.Context, content []agent.ContentPart) error {
	c.prepareCallbacks()
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
	c.prepareCallbacks()
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
	messages := c.steering.restore(c.controls.ClearSteering)
	c.model.restoreSteering(messages)
	if len(messages) > 0 {
		c.model.conversationVersion++
	}
}

func (c *tuiController) interruptTurn() {
	c.model.interrupted = true
	c.cancelTurn()
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
	return c.saveCheckpoint(checkpointModel(c.model, c.steering.pending()), active)
}
