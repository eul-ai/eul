package terminal

import (
	"errors"
	"math"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestClipboardImageLimitFitsRequestLimit(t *testing.T) {
	encodedSize := int64(math.Ceil(float64(maxAttachedImagesTotalBytes)/3) * 4)
	if encodedSize >= 32*1024*1024 {
		t.Fatalf("encoded image size = %d", encodedSize)
	}
}

func TestAttachImageEnforcesLimits(t *testing.T) {
	model := newTestModel(80, 24, Options{})
	for range maxAttachedImages {
		if err := model.attachImage(agent.Image{Data: []byte("png")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := model.attachImage(agent.Image{Data: []byte("png")}); !errors.Is(err, errTooManyImages) {
		t.Fatalf("image count error = %v", err)
	}

	model.clearInput()
	if err := model.attachImage(agent.Image{Data: make([]byte, maxAttachedImagesTotalBytes)}); err != nil {
		t.Fatal(err)
	}
	if err := model.attachImage(agent.Image{Data: []byte("png")}); !errors.Is(err, errImagesTooLarge) {
		t.Fatalf("image size error = %v", err)
	}
}

func (m *tuiModel) attachImage(image agent.Image) error {
	const requestID = ^uint64(0)
	m.reserveImage(requestID)
	if err := m.resolveImage(requestID, image); err != nil {
		m.removePendingImage(requestID)
		return err
	}
	return nil
}
