package terminal

import "fmt"

type terminalColor struct {
	red   uint8
	green uint8
	blue  uint8
}

type theme struct {
	background      terminalColor
	foreground      terminalColor
	accent          terminalColor
	muted           terminalColor
	error           terminalColor
	panelBackground terminalColor
	toolBackground  terminalColor
	editorLine      terminalColor
}

// Source: https://github.com/iodic/pi-ayu-themes/blob/main/themes/ayu-mirage.json
var ayuMirageTheme = theme{
	background:      terminalColor{red: 0x17, green: 0x1b, blue: 0x24},
	foreground:      terminalColor{red: 0xcc, green: 0xca, blue: 0xc2},
	accent:          terminalColor{red: 0xff, green: 0xcc, blue: 0x66},
	muted:           terminalColor{red: 0x84, green: 0x90, blue: 0xa5},
	error:           terminalColor{red: 0xf2, green: 0x87, blue: 0x79},
	panelBackground: terminalColor{red: 0x13, green: 0x16, blue: 0x1e},
	toolBackground:  terminalColor{red: 0x1a, green: 0x20, blue: 0x30},
	editorLine:      terminalColor{red: 0x1e, green: 0x24, blue: 0x30},
}

var currentTheme = ayuMirageTheme

func ansiColors(foreground, background terminalColor) string {
	return fmt.Sprintf(
		"\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm",
		foreground.red,
		foreground.green,
		foreground.blue,
		background.red,
		background.green,
		background.blue,
	)
}
