package terminal

import (
	"errors"
	"io"
	"time"
)

const escapeSequenceTimeout = 30 * time.Millisecond

type inputReadResult struct {
	data []byte
	err  error
}

func (d *keyDecoder) hasPendingEscape() bool {
	return !d.inPaste && len(d.buffer) > 0 && d.buffer[0] == '\x1b'
}

func (d *keyDecoder) flushPendingEscape() []keyEvent {
	if !d.hasPendingEscape() || len(d.buffer) != 1 {
		return nil
	}
	d.buffer = nil
	return []keyEvent{{code: keyEscape}}
}

func (d *keyDecoder) discardPendingEscape() {
	if d.hasPendingEscape() {
		d.buffer = nil
	}
}

func readKeyEvents(input io.Reader, output chan<- keyEvent, stopped <-chan struct{}) {
	reads := make(chan inputReadResult, 1)
	go readInput(input, reads, stopped)

	decoder := &keyDecoder{}
	var escapeTimer *time.Timer
	var escapeClock <-chan time.Time
	stopEscapeTimer := func() {
		if escapeTimer != nil {
			escapeTimer.Stop()
		}
		escapeTimer = nil
		escapeClock = nil
	}
	startEscapeTimer := func() {
		stopEscapeTimer()
		if decoder.hasPendingEscape() {
			escapeTimer = time.NewTimer(escapeSequenceTimeout)
			escapeClock = escapeTimer.C
		}
	}
	defer stopEscapeTimer()

	for {
		select {
		case result := <-reads:
			stopEscapeTimer()
			if !sendKeyEvents(output, decoder.feed(result.data, false), stopped) {
				return
			}
			if result.err == nil {
				startEscapeTimer()
				continue
			}

			if !sendKeyEvents(output, decoder.flushPendingEscape(), stopped) {
				return
			}
			decoder.discardPendingEscape()
			if !sendKeyEvents(output, decoder.feed(nil, true), stopped) {
				return
			}
			event := keyEvent{code: keyFailure, err: result.err, fatal: true}
			if errors.Is(result.err, io.EOF) {
				event = keyEvent{code: keyEOF}
			}
			select {
			case output <- event:
			case <-stopped:
			}
			return

		case <-escapeClock:
			escapeTimer = nil
			escapeClock = nil
			if !sendKeyEvents(output, decoder.flushPendingEscape(), stopped) {
				return
			}

		case <-stopped:
			return
		}
	}
}

func readInput(input io.Reader, output chan<- inputReadResult, stopped <-chan struct{}) {
	buffer := make([]byte, 4096)
	for {
		count, err := input.Read(buffer)
		result := inputReadResult{err: err}
		if count > 0 {
			result.data = append([]byte(nil), buffer[:count]...)
		}
		select {
		case output <- result:
		case <-stopped:
			return
		}
		if err != nil {
			return
		}
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
