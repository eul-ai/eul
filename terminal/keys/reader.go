package keys

import (
	"errors"
	"io"
	"time"
)

const escapeSequenceTimeout = 30 * time.Millisecond

type readResult struct {
	data []byte
	err  error
}

func (d *decoder) hasPendingEscape() bool {
	return !d.inPaste && len(d.buffer) > 0 && d.buffer[0] == '\x1b'
}

func (d *decoder) flushPendingEscape() []Event {
	if !d.hasPendingEscape() || len(d.buffer) != 1 {
		return nil
	}
	d.buffer = nil
	return []Event{{Code: Escape}}
}

func (d *decoder) discardPendingEscape() {
	if d.hasPendingEscape() {
		d.buffer = nil
	}
}

func Read(input io.Reader, output chan<- Event, stopped <-chan struct{}) {
	reads := make(chan readResult, 1)
	go readInput(input, reads, stopped)

	d := &decoder{}
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
		if d.hasPendingEscape() {
			escapeTimer = time.NewTimer(escapeSequenceTimeout)
			escapeClock = escapeTimer.C
		}
	}
	defer stopEscapeTimer()

	for {
		select {
		case result := <-reads:
			stopEscapeTimer()
			if !sendEvents(output, d.feed(result.data, false), stopped) {
				return
			}
			if result.err == nil {
				startEscapeTimer()
				continue
			}

			if !sendEvents(output, d.flushPendingEscape(), stopped) {
				return
			}
			d.discardPendingEscape()
			if !sendEvents(output, d.feed(nil, true), stopped) {
				return
			}
			event := Event{Code: Failure, Err: result.err, Fatal: true}
			if errors.Is(result.err, io.EOF) {
				event = Event{Code: EOF}
			}
			select {
			case output <- event:
			case <-stopped:
			}
			return

		case <-escapeClock:
			escapeTimer = nil
			escapeClock = nil
			if !sendEvents(output, d.flushPendingEscape(), stopped) {
				return
			}

		case <-stopped:
			return
		}
	}
}

func readInput(input io.Reader, output chan<- readResult, stopped <-chan struct{}) {
	buffer := make([]byte, 4096)
	for {
		count, err := input.Read(buffer)
		result := readResult{err: err}
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

func sendEvents(output chan<- Event, events []Event, stopped <-chan struct{}) bool {
	for _, event := range events {
		select {
		case output <- event:
		case <-stopped:
			return false
		}
	}
	return true
}
