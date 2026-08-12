package terminal

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestReadClipboardPNG(t *testing.T) {
	image, err := readClipboardPNG(bytes.NewReader([]byte("png")))
	if err != nil {
		t.Fatal(err)
	}
	if image.MediaType != "image/png" || string(image.Data) != "png" {
		t.Fatalf("image = %+v", image)
	}
}

func TestReadClipboardPNGRejectsOversizedImage(t *testing.T) {
	reader := bytes.NewReader(make([]byte, maxAttachedImageBytes+1))
	if _, err := readClipboardPNG(reader); !errors.Is(err, errClipboardImageTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestClipboardImageLimitFitsRequestLimit(t *testing.T) {
	encodedSize := int64(math.Ceil(float64(maxAttachedImagesTotalBytes)/3) * 4)
	if encodedSize >= 32*1024*1024 {
		t.Fatalf("encoded image size = %d", encodedSize)
	}
}

func TestAttachImageEnforcesLimits(t *testing.T) {
	model := newTUIModel(80, 24, Options{})
	model.images = make([]agent.Image, maxAttachedImages)
	if err := model.attachImage(agent.Image{Data: []byte("png")}); !errors.Is(err, errTooManyImages) {
		t.Fatalf("image count error = %v", err)
	}

	model.images = []agent.Image{{Data: make([]byte, maxAttachedImagesTotalBytes)}}
	if err := model.attachImage(agent.Image{Data: []byte("png")}); !errors.Is(err, errImagesTooLarge) {
		t.Fatalf("image size error = %v", err)
	}
}
