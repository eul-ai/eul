//go:build darwin

package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/eul-ai/eul/agent"
)

func readClipboardImage(ctx context.Context) (agent.Image, error) {
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

	const script = `on run argv
try
set imageData to the clipboard as «class PNGf»
on error
return "no image"
end try
set outputPath to item 1 of argv
set outputFile to open for access POSIX file outputPath with write permission
try
set eof outputFile to 0
write imageData to outputFile
close access outputFile
on error message
try
close access outputFile
end try
error message
end try
return "ok"
end run`
	output, err := exec.CommandContext(ctx, "osascript", "-e", script, path).Output()
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	if strings.TrimSpace(string(output)) != "ok" {
		return agent.Image{}, errClipboardImageUnavailable
	}

	file, err = os.Open(path)
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	defer file.Close()
	image, err := readClipboardPNG(file)
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	return image, nil
}
