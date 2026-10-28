package terminal

import (
	"fmt"

	"github.com/eul-ai/eul/agent"
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
	green                 terminalColor
	yellow                terminalColor
	muted                 terminalColor
	dimmed                terminalColor
	markdownCode          terminalColor
	error                 terminalColor
	diffAdded             terminalColor
	diffRemoved           terminalColor
	diffContext           terminalColor
	toolPendingBackground terminalColor
	toolSuccessBackground terminalColor
	toolErrorBackground   terminalColor
	editorLine            terminalColor
	selectedBackground    terminalColor
}

// Source: https://github.com/iodic/pi-ayu-themes/blob/main/themes/ayu-mirage.json
const (
	ayuMirageForeground = 0xcccac2
	ayuMirageAccent     = 0xffcc66
	ayuMirageOrange     = 0xffa759
	ayuMirageBlue       = 0x73d0ff
	ayuMirageCyan       = 0x95e6cb
	ayuMirageGreen      = 0xbae67e
	ayuMirageRed        = 0xf28779
	ayuMirageYellow     = 0xffe6b3
	ayuMirageMuted      = 0x8490a5
	ayuMirageDimmed     = 0x6a7687
	ayuMirageEditorLine = 0x1e2430
)

var ayuMirageTheme = theme{
	foreground:            rgb(ayuMirageForeground),
	accent:                rgb(ayuMirageAccent),
	orange:                rgb(ayuMirageOrange),
	blue:                  rgb(ayuMirageBlue),
	green:                 rgb(ayuMirageGreen),
	yellow:                rgb(ayuMirageYellow),
	muted:                 rgb(ayuMirageMuted),
	dimmed:                rgb(ayuMirageDimmed),
	markdownCode:          rgb(ayuMirageCyan),
	error:                 rgb(ayuMirageRed),
	diffAdded:             rgb(ayuMirageGreen),
	diffRemoved:           rgb(ayuMirageRed),
	diffContext:           rgb(ayuMirageMuted),
	toolPendingBackground: rgb(0x1a2030),
	toolSuccessBackground: rgb(0x1f282f),
	toolErrorBackground:   rgb(0x252029),
	editorLine:            rgb(ayuMirageEditorLine),
	selectedBackground:    rgb(0x161a24),
}

var currentTheme = ayuMirageTheme

func rgb(value uint32) terminalColor {
	return terminalColor{
		red:   uint8(value >> 16),
		green: uint8(value >> 8),
		blue:  uint8(value),
	}
}

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
