package tool

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
)

func setFinalBashPresentation(updates agent.ToolUpdateSink, command, output, outcome string, elapsed, timeout time.Duration) {
	if updates != nil {
		updates.SetFinal(bashOutputPresentation(command, output, outcome, elapsed, timeout))
	}
}

func bashOutputPresentation(command, output, outcome string, elapsed, timeout time.Duration) agent.ToolPresentation {
	presentation := bashPresentation(command, timeout)
	presentation.Outcome = outcome
	output = strings.ReplaceAll(output, "\r\n", "\n")
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		presentation.Lines = strings.Split(trimmed, "\n")
	}
	presentation.TailLines = bashPreviewLines
	presentation.Elapsed = max(time.Nanosecond, elapsed)
	return presentation
}

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

type bashOutputStreamer struct {
	capture  *tailCapture
	updates  agent.ToolUpdateSink
	command  string
	started  time.Time
	timeout  time.Duration
	cancel   context.CancelFunc
	dirty    chan struct{}
	stopNow  chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
	errMu    sync.Mutex
	err      error
}

func newBashOutputStreamer(
	capture *tailCapture,
	updates agent.ToolUpdateSink,
	command string,
	started time.Time,
	timeout time.Duration,
	cancel context.CancelFunc,
) *bashOutputStreamer {
	return &bashOutputStreamer{
		capture: capture,
		updates: updates,
		command: command,
		started: started,
		timeout: timeout,
		cancel:  cancel,
		dirty:   make(chan struct{}, 1),
		stopNow: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (s *bashOutputStreamer) start() {
	go s.run()
}

func (s *bashOutputStreamer) Write(data []byte) (int, error) {
	written, err := s.capture.Write(data)
	if written > 0 {
		select {
		case s.dirty <- struct{}{}:
		default:
		}
	}
	return written, err
}

func (s *bashOutputStreamer) run() {
	defer close(s.stopped)
	ticker := time.NewTicker(bashUpdateInterval)
	defer ticker.Stop()

	dirty := false
	lastElapsedSecond := int64(-1)
	for {
		select {
		case <-s.dirty:
			dirty = true
		case now := <-ticker.C:
			elapsed := now.Sub(s.started)
			elapsedSecond := int64(elapsed / time.Second)
			if !dirty && elapsedSecond == lastElapsedSecond {
				continue
			}
			output, _ := s.capture.String()
			if err := s.updates.Update(bashOutputPresentation(s.command, output, "", elapsed, s.timeout)); err != nil {
				s.errMu.Lock()
				s.err = err
				s.errMu.Unlock()
				s.cancel()
				return
			}
			dirty = false
			lastElapsedSecond = elapsedSecond
		case <-s.stopNow:
			return
		}
	}
}

func (s *bashOutputStreamer) stop() error {
	s.stopOnce.Do(func() {
		close(s.stopNow)
	})
	<-s.stopped
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}
