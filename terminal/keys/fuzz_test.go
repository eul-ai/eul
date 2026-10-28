package keys

import (
	"reflect"
	"testing"
)

type stableEvent struct {
	code  Code
	text  string
	mouse Mouse
	err   string
	fatal bool
}

func FuzzDecoderChunking(f *testing.F) {
	for _, seed := range []struct {
		input []byte
		split uint16
	}{
		{input: []byte("plain text"), split: 5},
		{input: []byte("héllo"), split: 2},
		{input: []byte("\x1b[A\x1b[13;2u"), split: 3},
		{input: []byte("\x1b[<0;10;20M"), split: 7},
		{input: []byte("\x1b[200~pasted\ntext\x1b[201~"), split: 14},
		{input: []byte{0xff, 0, 'x'}, split: 1},
		{input: []byte("\x1b["), split: 1},
	} {
		f.Add(seed.input, seed.split)
	}

	f.Fuzz(func(t *testing.T, input []byte, splitSeed uint16) {
		if len(input) > 16*1024 {
			t.Skip()
		}

		wholeDecoder := &decoder{}
		whole := stableEvents(wholeDecoder.feed(input, true))

		split := 0
		if len(input) > 0 {
			split = int(splitSeed) % (len(input) + 1)
		}
		splitDecoder := &decoder{}
		chunkedEvents := splitDecoder.feed(input[:split], false)
		chunkedEvents = append(chunkedEvents, splitDecoder.feed(input[split:], true)...)
		chunked := stableEvents(chunkedEvents)

		if !reflect.DeepEqual(chunked, whole) {
			t.Fatalf("split %d produced different events:\nwhole:   %#v\nchunked: %#v", split, whole, chunked)
		}
	})
}

func stableEvents(events []Event) []stableEvent {
	stable := make([]stableEvent, 0, len(events))
	for _, event := range events {
		current := stableEvent{
			code:  event.Code,
			text:  event.Text,
			mouse: event.Mouse,
			fatal: event.Fatal,
		}
		if event.Err != nil {
			current.err = event.Err.Error()
		}
		if current.code == Text && len(stable) > 0 && stable[len(stable)-1].code == Text {
			stable[len(stable)-1].text += current.text
			continue
		}
		stable = append(stable, current)
	}
	return stable
}
