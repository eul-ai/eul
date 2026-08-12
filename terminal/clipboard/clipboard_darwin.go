//go:build darwin

package clipboard

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/eul-ai/eul/agent"
)

//go:embed clipboard_darwin.applescript
var clipboardDarwinScript string

func readImage(ctx context.Context) (agent.Image, error) {
	file, err := os.CreateTemp("", "eul-clipboard-*.png")
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	defer os.Remove(path)

	output, err := exec.CommandContext(ctx, "osascript", "-e", clipboardDarwinScript, path).Output()
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	if strings.TrimSpace(string(output)) != "ok" {
		return agent.Image{}, errImageUnavailable
	}

	file, err = os.Open(path)
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	defer file.Close()
	image, err := readPNG(file)
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	return image, nil
}
