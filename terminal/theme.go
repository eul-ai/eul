package terminal

import (
	"fmt"

	"yaah/agent"
)

type terminalColor struct {
	red   uint8
	green uint8
	blue  uint8
}

type theme struct {
	foreground            terminalColor
	accent                terminalColor
	orange                terminalColor
	blue                  terminalColor
	markdownCode          terminalColor
	muted                 terminalColor
	dimmed                terminalColor
	error                 terminalColor
	toolPendingBackground terminalColor
	toolSuccessBackground terminalColor
	toolErrorBackground   terminalColor
	editorLine            terminalColor
}

// Source: https://github.com/iodic/pi-ayu-themes/blob/main/themes/ayu-mirage.json
var ayuMirageTheme = theme{
	foreground:            terminalColor{red: 0xcc, green: 0xca, blue: 0xc2},
	accent:                terminalColor{red: 0xff, green: 0xcc, blue: 0x66},
	orange:                terminalColor{red: 0xff, green: 0xa7, blue: 0x59},
	blue:                  terminalColor{red: 0x73, green: 0xd0, blue: 0xff},
	markdownCode:          terminalColor{red: 0x95, green: 0xe6, blue: 0xcb},
	muted:                 terminalColor{red: 0x84, green: 0x90, blue: 0xa5},
	dimmed:                terminalColor{red: 0x6a, green: 0x76, blue: 0x87},
	error:                 terminalColor{red: 0xf2, green: 0x87, blue: 0x79},
	toolPendingBackground: terminalColor{red: 0x1a, green: 0x20, blue: 0x30},
	toolSuccessBackground: terminalColor{red: 0x1f, green: 0x28, blue: 0x2f},
	toolErrorBackground:   terminalColor{red: 0x25, green: 0x20, blue: 0x29},
	editorLine:            terminalColor{red: 0x1e, green: 0x24, blue: 0x30},
}

var currentTheme = ayuMirageTheme

func (t theme) thinkingColor(level agent.ThinkingLevel) terminalColor {
	switch level {
	case agent.ThinkingOff:
		return t.dimmed
	case agent.ThinkingLow:
		return t.blue
	case agent.ThinkingMedium:
		return t.accent
	case agent.ThinkingHigh:
		return t.orange
	case agent.ThinkingXHigh, agent.ThinkingMax:
		return t.error
	default:
		return t.muted
	}
}

func ansiColors(foreground, background terminalColor, paintBackground bool) string {
	colors := ansiForeground(foreground)
	if !paintBackground {
		return colors + "\x1b[49m"
	}
	return colors + fmt.Sprintf(
		"\x1b[48;2;%d;%d;%dm",
		background.red,
		background.green,
		background.blue,
	)
}

func ansiForeground(color terminalColor) string {
	return fmt.Sprintf(
		"\x1b[38;2;%d;%d;%dm",
		color.red,
		color.green,
		color.blue,
	)
}
