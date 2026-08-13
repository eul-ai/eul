package clipboard

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, 1, 1))
	value.Set(0, 0, color.White)
	if err := png.Encode(&encoded, value); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestReadPNG(t *testing.T) {
	data := testPNG(t)
	image, err := readPNG(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if image.MediaType != "image/png" || !bytes.Equal(image.Data, data) {
		t.Fatalf("image = %+v", image)
	}
}

func TestValidateImageRejectsMediaTypeMismatch(t *testing.T) {
	if err := ValidateImage(agent.Image{MediaType: "image/jpeg", Data: testPNG(t)}); !errors.Is(err, errImageInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadPNGRejectsInvalidImage(t *testing.T) {
	if _, err := readPNG(bytes.NewReader([]byte("png"))); !errors.Is(err, errImageInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadPNGRejectsTruncatedImage(t *testing.T) {
	data := testPNG(t)
	if _, err := readPNG(bytes.NewReader(data[:len(data)-1])); !errors.Is(err, errImageInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadPNGRejectsOversizedImage(t *testing.T) {
	reader := bytes.NewReader(make([]byte, maxImageBytes+1))
	if _, err := readPNG(reader); !errors.Is(err, errImageTooLarge) {
		t.Fatalf("error = %v", err)
	}
}
