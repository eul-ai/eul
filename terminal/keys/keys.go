package keys

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxPasteBytes = 1024 * 1024

var (
	ErrInputTooLong = errors.New("input is too long")
	ErrInvalidInput = errors.New("input must be valid UTF-8 text without NUL")
)

type Code uint8

const (
	Text Code = iota
	Enter
	Newline
	Tab
	ShiftTab
	Escape
	Left
	Right
	Up
	AltUp
	Down
	AltDown
	Home
	End
	Backspace
	Delete
	PageUp
	PageDown
	CtrlC
	CtrlD
	CtrlL
	CtrlV
	MouseInput
	EOF
	Failure
)

type Event struct {
	Code  Code
	Text  string
	Mouse Mouse
	Err   error
	Fatal bool
}

type MouseKind uint8

const (
	MousePress MouseKind = iota
	MouseDrag
	MouseRelease
	MouseWheelUp
	MouseWheelDown
)

type Mouse struct {
	Kind   MouseKind
	Column int
	Row    int
}

type decoder struct {
	buffer       []byte
	paste        []byte
	inPaste      bool
	pasteTooLong bool
}

var sequences = []struct {
	sequence string
	code     Code
}{
	{sequence: "\x1b[13;2~", code: Newline},
	{sequence: "\x1b[27;2;13~", code: Newline},
	{sequence: "\x1b\r", code: Newline},
	{sequence: "\x1bOM", code: Enter},
	{sequence: "\x1b[Z", code: ShiftTab},
	{sequence: "\x1b[27;2;9~", code: ShiftTab},
	{sequence: "\x1b[200~", code: Text},
	{sequence: "\x1b[1~", code: Home},
	{sequence: "\x1b[4~", code: End},
	{sequence: "\x1b[7~", code: Home},
	{sequence: "\x1b[8~", code: End},
	{sequence: "\x1b[3~", code: Delete},
	{sequence: "\x1b[5~", code: PageUp},
	{sequence: "\x1b[6~", code: PageDown},
	{sequence: "\x1b[1;3A", code: AltUp},
	{sequence: "\x1b[1;3B", code: AltDown},
	{sequence: "\x1b[A", code: Up},
	{sequence: "\x1b[B", code: Down},
	{sequence: "\x1b[C", code: Right},
	{sequence: "\x1b[D", code: Left},
	{sequence: "\x1b[H", code: Home},
	{sequence: "\x1b[F", code: End},
	{sequence: "\x1bOA", code: Up},
	{sequence: "\x1bOB", code: Down},
	{sequence: "\x1bOC", code: Right},
	{sequence: "\x1bOD", code: Left},
	{sequence: "\x1bOH", code: Home},
	{sequence: "\x1bOF", code: End},
}

const (
	kittyKeypadEnter = 57414
	pasteStart       = "\x1b[200~"
	pasteEnd         = "\x1b[201~"
)

func (d *decoder) feed(data []byte, final bool) []Event {
	d.buffer = append(d.buffer, data...)
	var events []Event

	for len(d.buffer) > 0 {
		if d.inPaste {
			end := bytes.Index(d.buffer, []byte(pasteEnd))
			if end < 0 {
				if final {
					d.appendPaste(d.buffer)
					d.buffer = nil
					events = append(events, d.finishPaste()...)
					continue
				}

				keep := len(pasteEnd) - 1
				if len(d.buffer) <= keep {
					break
				}
				d.appendPaste(d.buffer[:len(d.buffer)-keep])
				d.buffer = append(d.buffer[:0], d.buffer[len(d.buffer)-keep:]...)
				break
			}

			d.appendPaste(d.buffer[:end])
			d.buffer = d.buffer[end+len(pasteEnd):]
			events = append(events, d.finishPaste()...)
			continue
		}

		if d.buffer[0] == '\x1b' {
			consumed, mousePartial, event, emit := matchMouseSequence(d.buffer)
			if consumed > 0 {
				d.buffer = d.buffer[consumed:]
				if emit {
					events = append(events, event)
				}
				continue
			}

			matched, partial, event := matchSequence(d.buffer)
			if matched {
				d.buffer = d.buffer[len(event.Text):]
				if event.Text == pasteStart {
					d.inPaste = true
					d.paste = nil
					d.pasteTooLong = false
					continue
				}
				event.Text = ""
				events = append(events, event)
				continue
			}
			consumed, kittyPartial, event, emit := matchKittySequence(d.buffer)
			if consumed > 0 {
				d.buffer = d.buffer[consumed:]
				if emit {
					events = append(events, event)
				}
				continue
			}
			if (mousePartial || partial || kittyPartial) && !final {
				break
			}
			if consumed, complete := consumeUnknownEscape(d.buffer); complete {
				d.buffer = d.buffer[consumed:]
				continue
			}
			if !final {
				break
			}
			d.buffer = d.buffer[1:]
			continue
		}

		character := d.buffer[0]
		switch character {
		case 0:
			events = append(events, Event{Code: Failure, Err: ErrInvalidInput})
			d.buffer = d.buffer[1:]
			continue
		case '\r':
			events = append(events, Event{Code: Enter})
			d.buffer = d.buffer[1:]
			continue
		case '\n':
			events = append(events, Event{Code: Newline})
			d.buffer = d.buffer[1:]
			continue
		case '\t':
			events = append(events, Event{Code: Tab})
			d.buffer = d.buffer[1:]
			continue
		case 0x01:
			events = append(events, Event{Code: Home})
			d.buffer = d.buffer[1:]
			continue
		case 0x05:
			events = append(events, Event{Code: End})
			d.buffer = d.buffer[1:]
			continue
		case 0x7f, 0x08:
			events = append(events, Event{Code: Backspace})
			d.buffer = d.buffer[1:]
			continue
		case 0x03:
			events = append(events, Event{Code: CtrlC})
			d.buffer = d.buffer[1:]
			continue
		case 0x04:
			events = append(events, Event{Code: CtrlD})
			d.buffer = d.buffer[1:]
			continue
		case 0x0c:
			events = append(events, Event{Code: CtrlL})
			d.buffer = d.buffer[1:]
			continue
		case 0x16:
			events = append(events, Event{Code: CtrlV})
			d.buffer = d.buffer[1:]
			continue
		}

		if !utf8.FullRune(d.buffer) {
			if !final {
				break
			}
			events = append(events, Event{Code: Failure, Err: ErrInvalidInput})
			d.buffer = d.buffer[1:]
			continue
		}

		r, size := utf8.DecodeRune(d.buffer)
		if r == utf8.RuneError && size == 1 {
			events = append(events, Event{Code: Failure, Err: ErrInvalidInput})
			d.buffer = d.buffer[1:]
			continue
		}
		d.buffer = d.buffer[size:]
		if unicode.IsControl(r) {
			continue
		}
		events = appendTextEvent(events, string(r))
	}

	return events
}

func (d *decoder) appendPaste(content []byte) {
	if d.pasteTooLong {
		return
	}
	if len(d.paste)+len(content) > MaxPasteBytes {
		d.paste = nil
		d.pasteTooLong = true
		return
	}
	d.paste = append(d.paste, content...)
}

func (d *decoder) finishPaste() []Event {
	d.inPaste = false
	if d.pasteTooLong {
		d.pasteTooLong = false
		return []Event{{Code: Failure, Err: ErrInputTooLong}}
	}

	content := string(d.paste)
	d.paste = nil
	if !utf8.ValidString(content) || bytes.IndexByte([]byte(content), 0) >= 0 {
		return []Event{{Code: Failure, Err: ErrInvalidInput}}
	}
	if content == "" {
		return nil
	}
	return []Event{{Code: Text, Text: content}}
}

func matchSequence(buffer []byte) (bool, bool, Event) {
	partial := false
	for _, key := range sequences {
		sequence := []byte(key.sequence)
		if bytes.HasPrefix(buffer, sequence) {
			return true, false, Event{Code: key.code, Text: key.sequence}
		}
		if bytes.HasPrefix(sequence, buffer) {
			partial = true
		}
	}
	return false, partial, Event{}
}

func matchMouseSequence(buffer []byte) (int, bool, Event, bool) {
	consumed, sgrPartial, event, emit := matchSGRMouseSequence(buffer)
	if consumed > 0 {
		return consumed, false, event, emit
	}
	consumed, x10Partial, event, emit := matchX10MouseSequence(buffer)
	if consumed > 0 {
		return consumed, false, event, emit
	}
	return 0, sgrPartial || x10Partial, Event{}, false
}

func matchSGRMouseSequence(buffer []byte) (int, bool, Event, bool) {
	prefix := []byte("\x1b[<")
	if bytes.HasPrefix(prefix, buffer) {
		return 0, true, Event{}, false
	}
	if !bytes.HasPrefix(buffer, prefix) {
		return 0, false, Event{}, false
	}

	final := -1
	for index := len(prefix); index < len(buffer); index++ {
		if buffer[index] < 0x40 || buffer[index] > 0x7e {
			continue
		}
		if buffer[index] != 'M' && buffer[index] != 'm' {
			return index + 1, false, Event{}, false
		}
		final = index
		break
	}
	if final < 0 {
		return 0, true, Event{}, false
	}

	consumed := final + 1
	parameters := strings.Split(string(buffer[len(prefix):final]), ";")
	if len(parameters) != 3 {
		return consumed, false, Event{}, false
	}
	button, buttonErr := strconv.Atoi(parameters[0])
	column, columnErr := strconv.Atoi(parameters[1])
	row, rowErr := strconv.Atoi(parameters[2])
	if buttonErr != nil || columnErr != nil || rowErr != nil || column < 1 || row < 1 {
		return consumed, false, Event{}, false
	}

	mouse := Mouse{Column: column - 1, Row: row - 1}
	switch {
	case button&64 != 0 && button&3 == 0:
		mouse.Kind = MouseWheelUp
	case button&64 != 0 && button&3 == 1:
		mouse.Kind = MouseWheelDown
	case buffer[final] == 'm' && button&3 == 0:
		mouse.Kind = MouseRelease
	case button&3 != 0 || buffer[final] == 'm':
		return consumed, false, Event{}, false
	case button&32 != 0:
		mouse.Kind = MouseDrag
	default:
		mouse.Kind = MousePress
	}
	return consumed, false, Event{Code: MouseInput, Mouse: mouse}, true
}

func matchX10MouseSequence(buffer []byte) (int, bool, Event, bool) {
	prefix := []byte("\x1b[M")
	if bytes.HasPrefix(prefix, buffer) {
		return 0, true, Event{}, false
	}
	if !bytes.HasPrefix(buffer, prefix) {
		return 0, false, Event{}, false
	}
	if len(buffer) < 6 {
		return 0, true, Event{}, false
	}

	button := int(buffer[3]) - 32
	column := int(buffer[4]) - 33
	row := int(buffer[5]) - 33
	if button < 0 || column < 0 || row < 0 {
		return 6, false, Event{}, false
	}
	mouse := Mouse{Column: column, Row: row}
	switch {
	case button&64 != 0 && button&3 == 0:
		mouse.Kind = MouseWheelUp
	case button&64 != 0 && button&3 == 1:
		mouse.Kind = MouseWheelDown
	case button&3 == 3:
		mouse.Kind = MouseRelease
	case button&3 != 0:
		return 6, false, Event{}, false
	case button&32 != 0:
		mouse.Kind = MouseDrag
	default:
		mouse.Kind = MousePress
	}
	return 6, false, Event{Code: MouseInput, Mouse: mouse}, true
}

func matchKittySequence(buffer []byte) (int, bool, Event, bool) {
	if len(buffer) < 2 || buffer[0] != '\x1b' || buffer[1] != '[' {
		return 0, false, Event{}, false
	}

	final := -1
	for index := 2; index < len(buffer); index++ {
		if buffer[index] >= 0x40 && buffer[index] <= 0x7e {
			final = index
			break
		}
	}
	if final < 0 {
		return 0, true, Event{}, false
	}
	finalByte := buffer[final]
	if finalByte != 'u' && finalByte != '~' && !strings.ContainsRune("ABCDFHZ", rune(finalByte)) {
		return 0, false, Event{}, false
	}

	consumed := final + 1
	parameters := strings.Split(string(buffer[2:final]), ";")
	codepoints := strings.Split(parameters[0], ":")
	codepoint, err := strconv.Atoi(codepoints[0])
	if err != nil {
		return consumed, false, Event{}, false
	}

	modifier := 1
	eventType := 1
	if len(parameters) > 1 {
		modifiers := strings.Split(parameters[1], ":")
		modifier, err = strconv.Atoi(modifiers[0])
		if err != nil || modifier < 1 {
			return consumed, false, Event{}, false
		}
		if len(modifiers) > 1 {
			eventType, err = strconv.Atoi(modifiers[1])
			if err != nil {
				return consumed, false, Event{}, false
			}
		}
	}
	if eventType == 3 {
		return consumed, false, Event{}, false
	}

	modifier--
	shift := modifier&1 != 0
	alt := modifier&2 != 0
	control := modifier&4 != 0
	super := modifier&8 != 0
	if finalByte != 'u' {
		switch finalByte {
		case 'A':
			if alt {
				return consumed, false, Event{Code: AltUp}, true
			}
			return consumed, false, Event{Code: Up}, true
		case 'B':
			if alt {
				return consumed, false, Event{Code: AltDown}, true
			}
			return consumed, false, Event{Code: Down}, true
		case 'C':
			return consumed, false, Event{Code: Right}, true
		case 'D':
			return consumed, false, Event{Code: Left}, true
		case 'H':
			return consumed, false, Event{Code: Home}, true
		case 'F':
			return consumed, false, Event{Code: End}, true
		case 'Z':
			if shift {
				return consumed, false, Event{Code: ShiftTab}, true
			}
		case '~':
			switch codepoint {
			case 3:
				return consumed, false, Event{Code: Delete}, true
			case 5:
				return consumed, false, Event{Code: PageUp}, true
			case 6:
				return consumed, false, Event{Code: PageDown}, true
			case 7:
				return consumed, false, Event{Code: Home}, true
			case 8:
				return consumed, false, Event{Code: End}, true
			}
		}
		return consumed, false, Event{}, false
	}

	switch {
	case codepoint == 32 && shift:
		return consumed, false, Event{Code: Text, Text: " "}, true
	case (codepoint == 13 || codepoint == kittyKeypadEnter) && shift:
		return consumed, false, Event{Code: Newline}, true
	case codepoint == 13 || codepoint == kittyKeypadEnter:
		return consumed, false, Event{Code: Enter}, true
	case codepoint == 9 && shift:
		return consumed, false, Event{Code: ShiftTab}, true
	case codepoint == 9:
		return consumed, false, Event{Code: Tab}, true
	case codepoint == 27:
		return consumed, false, Event{Code: Escape}, true
	case (control && codepoint == 97) || codepoint == 1:
		return consumed, false, Event{Code: Home}, true
	case (control && codepoint == 99) || codepoint == 3:
		return consumed, false, Event{Code: CtrlC}, true
	case (control && codepoint == 100) || codepoint == 4:
		return consumed, false, Event{Code: CtrlD}, true
	case (control && codepoint == 101) || codepoint == 5:
		return consumed, false, Event{Code: End}, true
	case (control && codepoint == 108) || codepoint == 12:
		return consumed, false, Event{Code: CtrlL}, true
	case ((control || super) && codepoint == 118) || codepoint == 22:
		return consumed, false, Event{Code: CtrlV}, true
	case codepoint == 127:
		return consumed, false, Event{Code: Backspace}, true
	default:
		return consumed, false, Event{}, false
	}
}

func consumeUnknownEscape(buffer []byte) (int, bool) {
	if len(buffer) < 2 {
		return 0, false
	}
	if buffer[1] != '[' && buffer[1] != 'O' {
		return 1, true
	}

	for index := 2; index < len(buffer); index++ {
		if buffer[index] >= 0x40 && buffer[index] <= 0x7e {
			return index + 1, true
		}
	}
	return 0, false
}

func appendTextEvent(events []Event, text string) []Event {
	if len(events) > 0 && events[len(events)-1].Code == Text {
		events[len(events)-1].Text += text
		return events
	}
	return append(events, Event{Code: Text, Text: text})
}
