//go:build !darwin && !linux

package clipboard

import (
	"context"
	"errors"

	"github.com/eul-ai/eul/agent"
)

var errImageUnsupported = errors.New("clipboard images are not supported on this platform")

func readImage(context.Context) (agent.Image, error) {
	return agent.Image{}, errImageUnsupported
}
