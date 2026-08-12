//go:build !darwin && !linux

package terminal

import (
	"context"

	"github.com/eul-ai/eul/agent"
)

func readClipboardImage(context.Context) (agent.Image, error) {
	return agent.Image{}, errClipboardImageUnsupported
}
