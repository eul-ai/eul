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
	background            terminalColor
	foreground            terminalColor
	accent                terminalColor
	orange                terminalColor
	blue                  terminalColor
	cyan                  terminalColor
	green                 terminalColor
	red                   terminalColor
	purple                terminalColor
	yellow                terminalColor
	operator              terminalColor
	muted                 terminalColor
	dimmed                terminalColor
	panelBackground       terminalColor
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
	ayuMirageBackground      = 0x171b24
	ayuMirageForeground      = 0xcccac2
	ayuMirageAccent          = 0xffcc66
	ayuMirageOrange          = 0xffa759
	ayuMirageBlue            = 0x73d0ff
	ayuMirageCyan            = 0x95e6cb
	ayuMirageGreen           = 0xbae67e
	ayuMirageRed             = 0xf28779
	ayuMiragePurple          = 0xdfbfff
	ayuMirageYellow          = 0xffe6b3
	ayuMirageOperator        = 0xf29e74
	ayuMirageMuted           = 0x8490a5
	ayuMirageDimmed          = 0x6a7687
	ayuMiragePanelBackground = 0x13161e
	ayuMirageEditorLine      = 0x1e2430
)

var ayuMirageTheme = theme{
	background:            rgb(ayuMirageBackground),
	foreground:            rgb(ayuMirageForeground),
	accent:                rgb(ayuMirageAccent),
	orange:                rgb(ayuMirageOrange),
	blue:                  rgb(ayuMirageBlue),
	cyan:                  rgb(ayuMirageCyan),
	green:                 rgb(ayuMirageGreen),
	red:                   rgb(ayuMirageRed),
	purple:                rgb(ayuMiragePurple),
	yellow:                rgb(ayuMirageYellow),
	operator:              rgb(ayuMirageOperator),
	muted:                 rgb(ayuMirageMuted),
	dimmed:                rgb(ayuMirageDimmed),
	panelBackground:       rgb(ayuMiragePanelBackground),
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
