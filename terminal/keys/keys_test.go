package keys

import (
	"errors"
	"strings"
	"testing"
)

func TestDecoderHandlesTextAndSplitSequences(t *testing.T) {
	d := &decoder{}
	events := d.feed([]byte("hé\x1b["), false)
	events = append(events, d.feed([]byte("D\x7f\r"), false)...)

	want := []Event{
		{Code: Text, Text: "hé"},
		{Code: Left},
		{Code: Backspace},
		{Code: Enter},
	}
	assertEvents(t, events, want)
}

func TestDecoderRecognizesTabAndEscape(t *testing.T) {
	d := &decoder{}
	assertEvents(t, d.feed([]byte("\t\x1b[27u"), false), []Event{{Code: Tab}, {Code: Escape}})

	d = &decoder{}
	if events := d.feed([]byte("\x1b"), false); len(events) != 0 {
		t.Fatalf("partial escape events = %+v", events)
	}
	assertEvents(t, d.flushPendingEscape(), []Event{{Code: Escape}})

	d = &decoder{}
	if events := d.feed([]byte("\x1b["), false); len(events) != 0 {
		t.Fatalf("partial arrow events = %+v", events)
	}
	if events := d.flushPendingEscape(); len(events) != 0 {
		t.Fatalf("timed-out partial arrow events = %+v", events)
	}
	assertEvents(t, d.feed([]byte("A"), false), []Event{{Code: Up}})
}

func TestDecoderHandlesModifiedKeys(t *testing.T) {
	for sequence, code := range map[string]Code{
		"\x1b[13;2u":      Newline,
		"\x1b[13;2:1u":    Newline,
		"\x1b[13:13;2:1u": Newline,
		"\x1b[13;2~":      Newline,
		"\x1b[27;2;13~":   Newline,
		"\x1b\r":          Newline,
		"\n":              Newline,
		"\x1b[13u":        Enter,
		"\x1b[57414u":     Enter,
		"\x1b[57414;1:1u": Enter,
		"\x1bOM":          Enter,
		"\x1b[57414;2u":   Newline,
		"\x1b[57414;2:1u": Newline,
		"\x1b[Z":          ShiftTab,
		"\x1b[9;2u":       ShiftTab,
		"\x1b[9;2:1u":     ShiftTab,
		"\x1b[27;2;9~":    ShiftTab,
		"\x01":            Home,
		"\x1b[97;5u":      Home,
		"\x1b[1u":         Home,
		"\x05":            End,
		"\x1b[101;5u":     End,
		"\x1b[5u":         End,
		"\x1b[99;5u":      CtrlC,
		"\x1b[99;69:1u":   CtrlC,
		"\x1b[3u":         CtrlC,
		"\x1b[100;5u":     CtrlD,
		"\x1b[108;5u":     CtrlL,
		"\x16":            CtrlV,
		"\x1b[118;5u":     CtrlV,
		"\x1b[118;9u":     CtrlV,
		"\x1b[127u":       Backspace,
		"\x1b[1;1:1D":     Left,
		"\x1b[1;1:1C":     Right,
		"\x1b[1;1:1A":     Up,
		"\x1b[1;3A":       AltUp,
		"\x1b[1;3:1A":     AltUp,
		"\x1b[1;3B":       AltDown,
		"\x1b[1;3:1B":     AltDown,
		"\x1b[1;1:1B":     Down,
		"\x1b[1;1:1H":     Home,
		"\x1b[1;1:1F":     End,
		"\x1b[3;1:1~":     Delete,
		"\x1b[5;1:1~":     PageUp,
		"\x1b[6;1:1~":     PageDown,
	} {
		d := &decoder{}
		events := d.feed([]byte(sequence), false)
		assertEvents(t, events, []Event{{Code: code}})
	}
}

func TestDecoderTreatsShiftSpaceAsSpace(t *testing.T) {
	d := &decoder{}
	events := d.feed([]byte("\x1b[32;2u"), false)
	assertEvents(t, events, []Event{{Code: Text, Text: " "}})
}

func TestDecoderHandlesSGRMouseEvents(t *testing.T) {
	d := &decoder{}
	if events := d.feed([]byte("\x1b[<0;10"), false); len(events) != 0 {
		t.Fatalf("partial mouse events = %+v", events)
	}
	events := d.feed([]byte(";5M\x1b[<32;12;7M\x1b[<0;12;7m\x1b[<64;3;4M\x1b[<65;3;4M"), false)
	assertEvents(t, events, []Event{
		{Code: MouseInput, Mouse: Mouse{Kind: MousePress, Column: 9, Row: 4}},
		{Code: MouseInput, Mouse: Mouse{Kind: MouseDrag, Column: 11, Row: 6}},
		{Code: MouseInput, Mouse: Mouse{Kind: MouseRelease, Column: 11, Row: 6}},
		{Code: MouseInput, Mouse: Mouse{Kind: MouseWheelUp, Column: 2, Row: 3}},
		{Code: MouseInput, Mouse: Mouse{Kind: MouseWheelDown, Column: 2, Row: 3}},
	})

	if events := d.feed([]byte("\x1b[<2;1;1M\x1b[<2;1;1m"), false); len(events) != 0 {
		t.Fatalf("secondary-button events = %+v", events)
	}

	d = &decoder{}
	if events := d.feed([]byte("\x1b[M`#"), false); len(events) != 0 {
		t.Fatalf("partial X10 mouse events = %+v", events)
	}
	assertEvents(t, d.feed([]byte("$"), false), []Event{
		{Code: MouseInput, Mouse: Mouse{Kind: MouseWheelUp, Column: 2, Row: 3}},
	})
}

func TestDecoderHandlesSplitKittySequenceAndIgnoresRelease(t *testing.T) {
	d := &decoder{}
	if events := d.feed([]byte("\x1b[13;2:"), false); len(events) != 0 {
		t.Fatalf("partial events = %+v", events)
	}
	events := d.feed([]byte("1u\x1b[99;5:3u\x1b[?7u"), false)
	assertEvents(t, events, []Event{{Code: Newline}})
}

func TestDecoderHandlesBracketedPaste(t *testing.T) {
	d := &decoder{}
	if events := d.feed([]byte("\x1b[200~first\n"), false); len(events) != 0 {
		t.Fatalf("initial events = %+v", events)
	}
	events := d.feed([]byte("second\x1b[201~x"), false)

	assertEvents(t, events, []Event{{Code: Text, Text: "first\nsecondx"}})
}

func TestDecoderIgnoresUnknownEscapeSequence(t *testing.T) {
	d := &decoder{}
	events := d.feed([]byte("a\x1b[31mb"), false)
	assertEvents(t, events, []Event{{Code: Text, Text: "ab"}})
}

func TestDecoderRejectsInvalidAndOversizedPaste(t *testing.T) {
	d := &decoder{}
	events := d.feed([]byte{0xff, 0}, true)
	if len(events) != 2 || !errors.Is(events[0].Err, ErrInvalidInput) || !errors.Is(events[1].Err, ErrInvalidInput) {
		t.Fatalf("invalid events = %+v", events)
	}

	d = &decoder{}
	content := "\x1b[200~" + strings.Repeat("x", MaxPasteBytes+1) + "\x1b[201~"
	events = d.feed([]byte(content), false)
	if len(events) != 1 || !errors.Is(events[0].Err, ErrInputTooLong) {
		t.Fatalf("oversized paste events = %+v", events)
	}
}

func assertEvents(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index].Code != want[index].Code || got[index].Text != want[index].Text || got[index].Mouse != want[index].Mouse || !errors.Is(got[index].Err, want[index].Err) {
			t.Fatalf("event %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
