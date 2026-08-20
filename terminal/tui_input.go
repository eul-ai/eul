package terminal

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type keyCode uint8

const (
	keyText keyCode = iota
	keyEnter
	keyNewline
	keyTab
	keyShiftTab
	keyEscape
	keyLeft
	keyRight
	keyUp
	keyAltUp
	keyDown
	keyAltDown
	keyHome
	keyEnd
	keyBackspace
	keyDelete
	keyPageUp
	keyPageDown
	keyCtrlC
	keyCtrlD
	keyCtrlL
	keyCtrlO
	keyCtrlV
	keyMouse
	keyEOF
	keyFailure
)

type keyEvent struct {
	code  keyCode
	text  string
	mouse mouseEvent
	err   error
	fatal bool
}

type mouseEventKind uint8

const (
	mousePress mouseEventKind = iota
	mouseDrag
	mouseRelease
	mouseWheelUp
	mouseWheelDown
)

type mouseEvent struct {
	kind   mouseEventKind
	column int
	row    int
}

type keyDecoder struct {
	buffer       []byte
	paste        []byte
	inPaste      bool
	pasteTooLong bool
}

var keySequences = []struct {
	sequence string
	code     keyCode
}{
	{sequence: "\x1b[13;2~", code: keyNewline},
	{sequence: "\x1b[27;2;13~", code: keyNewline},
	{sequence: "\x1b\r", code: keyNewline},
	{sequence: "\x1bOM", code: keyEnter},
	{sequence: "\x1b[Z", code: keyShiftTab},
	{sequence: "\x1b[27;2;9~", code: keyShiftTab},
	{sequence: "\x1b[200~", code: keyText},
	{sequence: "\x1b[1~", code: keyHome},
	{sequence: "\x1b[4~", code: keyEnd},
	{sequence: "\x1b[7~", code: keyHome},
	{sequence: "\x1b[8~", code: keyEnd},
	{sequence: "\x1b[3~", code: keyDelete},
	{sequence: "\x1b[5~", code: keyPageUp},
	{sequence: "\x1b[6~", code: keyPageDown},
	{sequence: "\x1b[1;3A", code: keyAltUp},
	{sequence: "\x1b[1;3B", code: keyAltDown},
	{sequence: "\x1b[A", code: keyUp},
	{sequence: "\x1b[B", code: keyDown},
	{sequence: "\x1b[C", code: keyRight},
	{sequence: "\x1b[D", code: keyLeft},
	{sequence: "\x1b[H", code: keyHome},
	{sequence: "\x1b[F", code: keyEnd},
	{sequence: "\x1bOA", code: keyUp},
	{sequence: "\x1bOB", code: keyDown},
	{sequence: "\x1bOC", code: keyRight},
	{sequence: "\x1bOD", code: keyLeft},
	{sequence: "\x1bOH", code: keyHome},
	{sequence: "\x1bOF", code: keyEnd},
}

const (
	kittyKeypadEnter = 57414
	pasteStart       = "\x1b[200~"
	pasteEnd         = "\x1b[201~"
)

func (d *keyDecoder) feed(data []byte, final bool) []keyEvent {
	d.buffer = append(d.buffer, data...)
	var events []keyEvent

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

			matched, partial, event := matchKeySequence(d.buffer)
			if matched {
				d.buffer = d.buffer[len(event.text):]
				if event.text == pasteStart {
					d.inPaste = true
					d.paste = nil
					d.pasteTooLong = false
					continue
				}
				event.text = ""
				events = append(events, event)
				continue
			}
			consumed, kittyPartial, event, emit := matchKittyKeySequence(d.buffer)
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
			events = append(events, keyEvent{code: keyFailure, err: errInvalidInput})
			d.buffer = d.buffer[1:]
			continue
		case '\r':
			events = append(events, keyEvent{code: keyEnter})
			d.buffer = d.buffer[1:]
			continue
		case '\n':
			events = append(events, keyEvent{code: keyNewline})
			d.buffer = d.buffer[1:]
			continue
		case '\t':
			events = append(events, keyEvent{code: keyTab})
			d.buffer = d.buffer[1:]
			continue
		case 0x01:
			events = append(events, keyEvent{code: keyHome})
			d.buffer = d.buffer[1:]
			continue
		case 0x05:
			events = append(events, keyEvent{code: keyEnd})
			d.buffer = d.buffer[1:]
			continue
		case 0x7f, 0x08:
			events = append(events, keyEvent{code: keyBackspace})
			d.buffer = d.buffer[1:]
			continue
		case 0x03:
			events = append(events, keyEvent{code: keyCtrlC})
			d.buffer = d.buffer[1:]
			continue
		case 0x04:
			events = append(events, keyEvent{code: keyCtrlD})
			d.buffer = d.buffer[1:]
			continue
		case 0x0c:
			events = append(events, keyEvent{code: keyCtrlL})
			d.buffer = d.buffer[1:]
			continue
		case 0x0f:
			events = append(events, keyEvent{code: keyCtrlO})
			d.buffer = d.buffer[1:]
			continue
		case 0x16:
			events = append(events, keyEvent{code: keyCtrlV})
			d.buffer = d.buffer[1:]
			continue
		}

		if !utf8.FullRune(d.buffer) {
			if !final {
				break
			}
			events = append(events, keyEvent{code: keyFailure, err: errInvalidInput})
			d.buffer = d.buffer[1:]
			continue
		}

		r, size := utf8.DecodeRune(d.buffer)
		if r == utf8.RuneError && size == 1 {
			events = append(events, keyEvent{code: keyFailure, err: errInvalidInput})
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

func (d *keyDecoder) appendPaste(content []byte) {
	if d.pasteTooLong {
		return
	}
	if len(d.paste)+len(content) > maxInputBytes {
		d.paste = nil
		d.pasteTooLong = true
		return
	}
	d.paste = append(d.paste, content...)
}

func (d *keyDecoder) finishPaste() []keyEvent {
	d.inPaste = false
	if d.pasteTooLong {
		d.pasteTooLong = false
		return []keyEvent{{code: keyFailure, err: errInputTooLong}}
	}

	content := string(d.paste)
	d.paste = nil
	if !utf8.ValidString(content) || bytes.IndexByte([]byte(content), 0) >= 0 {
		return []keyEvent{{code: keyFailure, err: errInvalidInput}}
	}
	if content == "" {
		return nil
	}
	return []keyEvent{{code: keyText, text: content}}
}

func matchKeySequence(buffer []byte) (bool, bool, keyEvent) {
	partial := false
	for _, key := range keySequences {
		sequence := []byte(key.sequence)
		if bytes.HasPrefix(buffer, sequence) {
			return true, false, keyEvent{code: key.code, text: key.sequence}
		}
		if bytes.HasPrefix(sequence, buffer) {
			partial = true
		}
	}
	return false, partial, keyEvent{}
}

func matchMouseSequence(buffer []byte) (int, bool, keyEvent, bool) {
	consumed, sgrPartial, event, emit := matchSGRMouseSequence(buffer)
	if consumed > 0 {
		return consumed, false, event, emit
	}
	consumed, x10Partial, event, emit := matchX10MouseSequence(buffer)
	if consumed > 0 {
		return consumed, false, event, emit
	}
	return 0, sgrPartial || x10Partial, keyEvent{}, false
}

func matchSGRMouseSequence(buffer []byte) (int, bool, keyEvent, bool) {
	prefix := []byte("\x1b[<")
	if bytes.HasPrefix(prefix, buffer) {
		return 0, true, keyEvent{}, false
	}
	if !bytes.HasPrefix(buffer, prefix) {
		return 0, false, keyEvent{}, false
	}

	final := -1
	for index := len(prefix); index < len(buffer); index++ {
		if buffer[index] < 0x40 || buffer[index] > 0x7e {
			continue
		}
		if buffer[index] != 'M' && buffer[index] != 'm' {
			return index + 1, false, keyEvent{}, false
		}
		final = index
		break
	}
	if final < 0 {
		return 0, true, keyEvent{}, false
	}

	consumed := final + 1
	parameters := strings.Split(string(buffer[len(prefix):final]), ";")
	if len(parameters) != 3 {
		return consumed, false, keyEvent{}, false
	}
	button, buttonErr := strconv.Atoi(parameters[0])
	column, columnErr := strconv.Atoi(parameters[1])
	row, rowErr := strconv.Atoi(parameters[2])
	if buttonErr != nil || columnErr != nil || rowErr != nil || column < 1 || row < 1 {
		return consumed, false, keyEvent{}, false
	}

	mouse := mouseEvent{column: column - 1, row: row - 1}
	switch {
	case button&64 != 0 && button&3 == 0:
		mouse.kind = mouseWheelUp
	case button&64 != 0 && button&3 == 1:
		mouse.kind = mouseWheelDown
	case buffer[final] == 'm' && button&3 == 0:
		mouse.kind = mouseRelease
	case button&3 != 0 || buffer[final] == 'm':
		return consumed, false, keyEvent{}, false
	case button&32 != 0:
		mouse.kind = mouseDrag
	default:
		mouse.kind = mousePress
	}
	return consumed, false, keyEvent{code: keyMouse, mouse: mouse}, true
}

func matchX10MouseSequence(buffer []byte) (int, bool, keyEvent, bool) {
	prefix := []byte("\x1b[M")
	if bytes.HasPrefix(prefix, buffer) {
		return 0, true, keyEvent{}, false
	}
	if !bytes.HasPrefix(buffer, prefix) {
		return 0, false, keyEvent{}, false
	}
	if len(buffer) < 6 {
		return 0, true, keyEvent{}, false
	}

	button := int(buffer[3]) - 32
	column := int(buffer[4]) - 33
	row := int(buffer[5]) - 33
	if button < 0 || column < 0 || row < 0 {
		return 6, false, keyEvent{}, false
	}
	mouse := mouseEvent{column: column, row: row}
	switch {
	case button&64 != 0 && button&3 == 0:
		mouse.kind = mouseWheelUp
	case button&64 != 0 && button&3 == 1:
		mouse.kind = mouseWheelDown
	case button&3 == 3:
		mouse.kind = mouseRelease
	case button&3 != 0:
		return 6, false, keyEvent{}, false
	case button&32 != 0:
		mouse.kind = mouseDrag
	default:
		mouse.kind = mousePress
	}
	return 6, false, keyEvent{code: keyMouse, mouse: mouse}, true
}

func matchKittyKeySequence(buffer []byte) (int, bool, keyEvent, bool) {
	if len(buffer) < 2 || buffer[0] != '\x1b' || buffer[1] != '[' {
		return 0, false, keyEvent{}, false
	}

	final := -1
	for index := 2; index < len(buffer); index++ {
		if buffer[index] >= 0x40 && buffer[index] <= 0x7e {
			final = index
			break
		}
	}
	if final < 0 {
		return 0, true, keyEvent{}, false
	}
	finalByte := buffer[final]
	if finalByte != 'u' && finalByte != '~' && !strings.ContainsRune("ABCDFHZ", rune(finalByte)) {
		return 0, false, keyEvent{}, false
	}

	consumed := final + 1
	parameters := strings.Split(string(buffer[2:final]), ";")
	codepoints := strings.Split(parameters[0], ":")
	codepoint, err := strconv.Atoi(codepoints[0])
	if err != nil {
		return consumed, false, keyEvent{}, false
	}

	modifier := 1
	eventType := 1
	if len(parameters) > 1 {
		modifiers := strings.Split(parameters[1], ":")
		modifier, err = strconv.Atoi(modifiers[0])
		if err != nil || modifier < 1 {
			return consumed, false, keyEvent{}, false
		}
		if len(modifiers) > 1 {
			eventType, err = strconv.Atoi(modifiers[1])
			if err != nil {
				return consumed, false, keyEvent{}, false
			}
		}
	}
	if eventType == 3 {
		return consumed, false, keyEvent{}, false
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
				return consumed, false, keyEvent{code: keyAltUp}, true
			}
			return consumed, false, keyEvent{code: keyUp}, true
		case 'B':
			if alt {
				return consumed, false, keyEvent{code: keyAltDown}, true
			}
			return consumed, false, keyEvent{code: keyDown}, true
		case 'C':
			return consumed, false, keyEvent{code: keyRight}, true
		case 'D':
			return consumed, false, keyEvent{code: keyLeft}, true
		case 'H':
			return consumed, false, keyEvent{code: keyHome}, true
		case 'F':
			return consumed, false, keyEvent{code: keyEnd}, true
		case 'Z':
			if shift {
				return consumed, false, keyEvent{code: keyShiftTab}, true
			}
		case '~':
			switch codepoint {
			case 3:
				return consumed, false, keyEvent{code: keyDelete}, true
			case 5:
				return consumed, false, keyEvent{code: keyPageUp}, true
			case 6:
				return consumed, false, keyEvent{code: keyPageDown}, true
			case 7:
				return consumed, false, keyEvent{code: keyHome}, true
			case 8:
				return consumed, false, keyEvent{code: keyEnd}, true
			}
		}
		return consumed, false, keyEvent{}, false
	}

	switch {
	case codepoint == 32 && shift:
		return consumed, false, keyEvent{code: keyText, text: " "}, true
	case (codepoint == 13 || codepoint == kittyKeypadEnter) && shift:
		return consumed, false, keyEvent{code: keyNewline}, true
	case codepoint == 13 || codepoint == kittyKeypadEnter:
		return consumed, false, keyEvent{code: keyEnter}, true
	case codepoint == 9 && shift:
		return consumed, false, keyEvent{code: keyShiftTab}, true
	case codepoint == 9:
		return consumed, false, keyEvent{code: keyTab}, true
	case codepoint == 27:
		return consumed, false, keyEvent{code: keyEscape}, true
	case (control && codepoint == 97) || codepoint == 1:
		return consumed, false, keyEvent{code: keyHome}, true
	case (control && codepoint == 99) || codepoint == 3:
		return consumed, false, keyEvent{code: keyCtrlC}, true
	case (control && codepoint == 100) || codepoint == 4:
		return consumed, false, keyEvent{code: keyCtrlD}, true
	case (control && codepoint == 101) || codepoint == 5:
		return consumed, false, keyEvent{code: keyEnd}, true
	case (control && codepoint == 108) || codepoint == 12:
		return consumed, false, keyEvent{code: keyCtrlL}, true
	case (control && codepoint == 111) || codepoint == 15:
		return consumed, false, keyEvent{code: keyCtrlO}, true
	case ((control || super) && codepoint == 118) || codepoint == 22:
		return consumed, false, keyEvent{code: keyCtrlV}, true
	case codepoint == 127:
		return consumed, false, keyEvent{code: keyBackspace}, true
	default:
		return consumed, false, keyEvent{}, false
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

func appendTextEvent(events []keyEvent, text string) []keyEvent {
	if len(events) > 0 && events[len(events)-1].code == keyText {
		events[len(events)-1].text += text
		return events
	}
	return append(events, keyEvent{code: keyText, text: text})
}
