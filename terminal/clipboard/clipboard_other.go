//go:build !darwin && !linux

package clipboard

import (
	"context"

	"github.com/eul-ai/eul/agent"
)

func readImage(context.Context) (agent.Image, error) {
	return agent.Image{}, errImageUnsupported
}
