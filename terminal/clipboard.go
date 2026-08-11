package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/eul-ai/eul/agent"
)

var errClipboardImageUnavailable = errors.New("clipboard does not contain an image")

func readClipboardImage(ctx context.Context) (agent.Image, error) {
	switch runtime.GOOS {
	case "darwin":
		return readDarwinClipboardImage(ctx)
	case "linux":
		return readLinuxClipboardImage(ctx)
	case "windows":
		return readWindowsClipboardImage(ctx)
	default:
		return agent.Image{}, errors.New("clipboard images are not supported on this platform")
	}
}

func readDarwinClipboardImage(ctx context.Context) (agent.Image, error) {
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

	data, err := os.ReadFile(path)
	if err != nil {
		return agent.Image{}, fmt.Errorf("read clipboard image: %w", err)
	}
	return agent.Image{MediaType: "image/png", Data: data}, nil
}

func readLinuxClipboardImage(ctx context.Context) (agent.Image, error) {
	commands := [][]string{
		{"wl-paste", "--no-newline", "--type", "image/png"},
		{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
	}
	for _, arguments := range commands {
		data, err := exec.CommandContext(ctx, arguments[0], arguments[1:]...).Output()
		if err == nil && len(data) > 0 {
			return agent.Image{MediaType: "image/png", Data: data}, nil
		}
	}
	return agent.Image{}, errClipboardImageUnavailable
}

func readWindowsClipboardImage(ctx context.Context) (agent.Image, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms
$image = [Windows.Forms.Clipboard]::GetImage()
if ($null -eq $image) { exit 2 }
$stream = New-Object IO.MemoryStream
$image.Save($stream, [Drawing.Imaging.ImageFormat]::Png)
[Console]::OpenStandardOutput().Write($stream.ToArray(), 0, $stream.Length)`
	command := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Run(); err != nil || output.Len() == 0 {
		return agent.Image{}, errClipboardImageUnavailable
	}
	return agent.Image{MediaType: "image/png", Data: output.Bytes()}, nil
}
