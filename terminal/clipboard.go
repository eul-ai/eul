package terminal

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	clipboardReadTimeout        = 5 * time.Second
	maxAttachedImages           = 10
	maxAttachedImageBytes       = 10 * 1024 * 1024
	maxAttachedImagesTotalBytes = 10 * 1024 * 1024
)

var (
	errClipboardImageUnavailable = errors.New("clipboard does not contain an image")
	errClipboardImageUnsupported = errors.New("clipboard images are not supported on this platform")
	errClipboardImageTooLarge    = fmt.Errorf("clipboard image exceeds %d MiB", maxAttachedImageBytes/(1024*1024))
	errTooManyImages             = fmt.Errorf("a prompt can include at most %d images", maxAttachedImages)
	errImagesTooLarge            = fmt.Errorf("attached images exceed %d MiB", maxAttachedImagesTotalBytes/(1024*1024))
)

func readClipboardPNG(reader io.Reader) (agent.Image, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxAttachedImageBytes+1))
	if err != nil {
		return agent.Image{}, err
	}
	switch {
	case len(data) == 0:
		return agent.Image{}, errClipboardImageUnavailable
	case len(data) > maxAttachedImageBytes:
		return agent.Image{}, errClipboardImageTooLarge
	default:
		return agent.Image{MediaType: "image/png", Data: data}, nil
	}
}
