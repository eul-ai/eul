package terminal

import (
	"bytes"
	"errors"
	"io"
	"unicode"
	"unicode/utf8"
)

type keyCode uint8

const (
	keyText keyCode = iota
	keyEnter
	keyNewline
	keyShiftTab
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyBackspace
	keyDelete
	keyPageUp
	keyPageDown
	keyCtrlC
	keyCtrlD
	keyCtrlL
	keyEOF
	keyFailure
)

type keyEvent struct {
	code  keyCode
	text  string
	err   error
	fatal bool
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
	{sequence: "\x1b[13;2u", code: keyNewline},
	{sequence: "\x1b[13;2~", code: keyNewline},
	{sequence: "\x1b[27;2;13~", code: keyNewline},
	{sequence: "\x1b\r", code: keyNewline},
	{sequence: "\x1b[13u", code: keyEnter},
	{sequence: "\x1b[Z", code: keyShiftTab},
	{sequence: "\x1b[9;2u", code: keyShiftTab},
	{sequence: "\x1b[27;2;9~", code: keyShiftTab},
	{sequence: "\x1b[99;5u", code: keyCtrlC},
	{sequence: "\x1b[100;5u", code: keyCtrlD},
	{sequence: "\x1b[108;5u", code: keyCtrlL},
	{sequence: "\x1b[127u", code: keyBackspace},
	{sequence: "\x1b[200~", code: keyText},
	{sequence: "\x1b[1~", code: keyHome},
	{sequence: "\x1b[4~", code: keyEnd},
	{sequence: "\x1b[7~", code: keyHome},
	{sequence: "\x1b[8~", code: keyEnd},
	{sequence: "\x1b[3~", code: keyDelete},
	{sequence: "\x1b[5~", code: keyPageUp},
	{sequence: "\x1b[6~", code: keyPageDown},
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
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
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
			if partial && !final {
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

func readKeyEvents(input io.Reader, output chan<- keyEvent, stopped <-chan struct{}) {
	decoder := &keyDecoder{}
	buffer := make([]byte, 4096)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			if !sendKeyEvents(output, decoder.feed(buffer[:count], false), stopped) {
				return
			}
		}
		if err == nil {
			continue
		}

		if !sendKeyEvents(output, decoder.feed(nil, true), stopped) {
			return
		}
		event := keyEvent{code: keyFailure, err: err, fatal: true}
		if errors.Is(err, io.EOF) {
			event = keyEvent{code: keyEOF}
		}
		select {
		case output <- event:
		case <-stopped:
		}
		return
	}
}

func sendKeyEvents(output chan<- keyEvent, events []keyEvent, stopped <-chan struct{}) bool {
	for _, event := range events {
		select {
		case output <- event:
		case <-stopped:
			return false
		}
	}
	return true
}
