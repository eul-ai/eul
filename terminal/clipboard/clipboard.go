package clipboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	readTimeout   = 5 * time.Second
	maxImageBytes = 10 * 1024 * 1024
)

var (
	errImageUnavailable = errors.New("clipboard does not contain an image")
	errImageUnsupported = errors.New("clipboard images are not supported on this platform")
	errImageTooLarge    = fmt.Errorf("clipboard image exceeds %d MiB", maxImageBytes/(1024*1024))
)

func ReadImage(ctx context.Context) (agent.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return readImage(ctx)
}

func readPNG(reader io.Reader) (agent.Image, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxImageBytes+1))
	if err != nil {
		return agent.Image{}, err
	}
	switch {
	case len(data) == 0:
		return agent.Image{}, errImageUnavailable
	case len(data) > maxImageBytes:
		return agent.Image{}, errImageTooLarge
	default:
		return agent.Image{MediaType: "image/png", Data: data}, nil
	}
}
