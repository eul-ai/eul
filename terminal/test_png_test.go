package terminal

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func validTestPNG(t *testing.T) []byte {
	t.Helper()

	var encoded bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, 1, 1))
	value.Set(0, 0, color.White)
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
