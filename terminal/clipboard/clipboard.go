package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	readTimeout   = 5 * time.Second
	maxImageBytes = 10 * 1024 * 1024
)

var (
	errImageUnavailable = errors.New("clipboard does not contain an image")
	errImageInvalid     = errors.New("clipboard image is not a valid PNG")
	errImageTooLarge    = fmt.Errorf("clipboard image exceeds %d MiB", maxImageBytes/(1024*1024))
)

func ReadImage(ctx context.Context) (agent.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return readImage(ctx)
}

func ValidateImage(image agent.Image) error {
	if image.MediaType != "image/png" {
		return errImageInvalid
	}
	if _, err := png.Decode(bytes.NewReader(image.Data)); err != nil {
		return errImageInvalid
	}
	return nil
}

func readPNG(reader io.Reader) (agent.Image, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxImageBytes+1))
	if err != nil {
		return agent.Image{}, err
	}
	switch {
	case len(data) == 0:
		return agent.Image{}, errImageUnavailable
	case len(data) > maxImageBytes:
		return agent.Image{}, errImageTooLarge
	}
	image := agent.Image{MediaType: "image/png", Data: data}
	if err := ValidateImage(image); err != nil {
		return agent.Image{}, err
	}
	return image, nil
}
