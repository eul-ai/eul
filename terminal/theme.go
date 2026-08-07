package terminal

import "fmt"

type terminalColor struct {
	red   uint8
	green uint8
	blue  uint8
}

type theme struct {
	background            terminalColor
	foreground            terminalColor
	accent                terminalColor
	orange                terminalColor
	blue                  terminalColor
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
	background:            terminalColor{red: 0x17, green: 0x1b, blue: 0x24},
	foreground:            terminalColor{red: 0xcc, green: 0xca, blue: 0xc2},
	accent:                terminalColor{red: 0xff, green: 0xcc, blue: 0x66},
	orange:                terminalColor{red: 0xff, green: 0xa7, blue: 0x59},
	blue:                  terminalColor{red: 0x73, green: 0xd0, blue: 0xff},
	muted:                 terminalColor{red: 0x84, green: 0x90, blue: 0xa5},
	dimmed:                terminalColor{red: 0x6a, green: 0x76, blue: 0x87},
	error:                 terminalColor{red: 0xf2, green: 0x87, blue: 0x79},
	toolPendingBackground: terminalColor{red: 0x1a, green: 0x20, blue: 0x30},
	toolSuccessBackground: terminalColor{red: 0x1f, green: 0x28, blue: 0x2f},
	toolErrorBackground:   terminalColor{red: 0x25, green: 0x20, blue: 0x29},
	editorLine:            terminalColor{red: 0x1e, green: 0x24, blue: 0x30},
}

var currentTheme = ayuMirageTheme

func (t theme) effortColor(effort string) terminalColor {
	switch effort {
	case "none":
		return t.dimmed
	case "low":
		return t.blue
	case "medium":
		return t.accent
	case "high":
		return t.orange
	case "xhigh", "max":
		return t.error
	default:
		return t.muted
	}
}

func ansiColors(foreground, background terminalColor, paintBackground bool) string {
	colors := fmt.Sprintf(
		"\x1b[38;2;%d;%d;%dm",
		foreground.red,
		foreground.green,
		foreground.blue,
	)
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
