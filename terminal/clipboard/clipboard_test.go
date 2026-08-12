package clipboard

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadPNG(t *testing.T) {
	image, err := readPNG(bytes.NewReader([]byte("png")))
	if err != nil {
		t.Fatal(err)
	}
	if image.MediaType != "image/png" || string(image.Data) != "png" {
		t.Fatalf("image = %+v", image)
	}
}

func TestReadPNGRejectsOversizedImage(t *testing.T) {
	reader := bytes.NewReader(make([]byte, maxImageBytes+1))
	if _, err := readPNG(reader); !errors.Is(err, errImageTooLarge) {
		t.Fatalf("error = %v", err)
	}
}
