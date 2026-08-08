package tool

import (
	"strings"
	"sync"
)

type tailCapture struct {
	mu        sync.Mutex
	data      []byte
	maxBytes  int
	truncated bool
}

func newTailCapture(maxBytes int) *tailCapture {
	return &tailCapture{maxBytes: maxBytes}
}

func (c *tailCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	originalLength := len(data)
	if c.maxBytes <= 0 {
		c.truncated = c.truncated || originalLength > 0
		return originalLength, nil
	}

	if len(data) > c.maxBytes {
		c.data = append(c.data[:0], data[len(data)-c.maxBytes:]...)
		c.truncated = true
		return originalLength, nil
	}

	if len(c.data)+len(data) > c.maxBytes {
		drop := len(c.data) + len(data) - c.maxBytes
		copy(c.data, c.data[drop:])
		c.data = c.data[:len(c.data)-drop]
		c.truncated = true
	}

	c.data = append(c.data, data...)
	return originalLength, nil
}

func (c *tailCapture) String() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.ToValidUTF8(string(c.data), "�"), c.truncated
}
