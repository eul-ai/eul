//go:build linux

package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/eul-ai/eul/agent"
)

func readImage(ctx context.Context) (agent.Image, error) {
	commands := [][]string{
		{"wl-paste", "--no-newline", "--type", "image/png"},
		{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
	}
	var commandFound bool
	for _, arguments := range commands {
		command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
		stdout, err := command.StdoutPipe()
		if err != nil {
			continue
		}
		if err := command.Start(); err != nil {
			if !errors.Is(err, exec.ErrNotFound) {
				commandFound = true
			}
			continue
		}
		commandFound = true

		image, readErr := readPNG(stdout)
		if errors.Is(readErr, errImageTooLarge) {
			_ = command.Process.Kill()
			_ = command.Wait()
			return agent.Image{}, readErr
		}
		waitErr := command.Wait()
		if readErr == nil && waitErr == nil {
			return image, nil
		}
		if err := ctx.Err(); err != nil {
			return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
		}
	}
	if !commandFound {
		return agent.Image{}, errors.New("clipboard images require wl-paste or xclip")
	}
	return agent.Image{}, errImageUnavailable
}
