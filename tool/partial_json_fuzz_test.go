package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
)

func FuzzParseStreamingJSONObject(f *testing.F) {
	for _, seed := range []string{
		"",
		"{",
		`{"path":"main.go","line":12}`,
		`{"path":"main.go","nested":{"ready":true},"items":[1,2,null]}`,
		`{"escaped":"line\n\u263a","partial":"value`,
		`{"number":1.25e+3} trailing`,
		`null`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64*1024 {
			t.Skip()
		}

		got := parseStreamingJSONObject(raw)
		if got == nil {
			t.Fatal("parseStreamingJSONObject returned nil")
		}

		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.UseNumber()
		var want map[string]any
		if err := decoder.Decode(&want); err != nil {
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return
		}
		if want == nil {
			want = map[string]any{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("complete object mismatch: got %#v, want %#v", got, want)
		}
	})
}
