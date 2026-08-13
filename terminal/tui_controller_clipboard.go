package terminal

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal/clipboard"
)

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
