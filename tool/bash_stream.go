package tool

import (
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
)

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
	truncated bool
	writes    int
}

func (c *tailCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.writes++
	c.data = append(c.data, data...)
	if excess := len(c.data) - defaultMaxBytes; excess > 0 {
		c.data = c.data[excess:]
		c.truncated = true
	}
	return len(data), nil
}

func (c *tailCapture) String() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.ToValidUTF8(string(c.data), "�"), c.truncated
}

func (c *tailCapture) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.writes
}

// streamBashOutput publishes the captured output on each tick where it or the
// elapsed second changed, until publish fails or the returned stop is called.
func streamBashOutput(capture *tailCapture, started time.Time, publish func(string, time.Duration) error) func() {
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(bashUpdateInterval)
		defer ticker.Stop()

		lastWrites, lastSecond := 0, time.Duration(-1)
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				elapsed := now.Sub(started)
				writes, second := capture.writeCount(), elapsed/time.Second
				if writes == lastWrites && second == lastSecond {
					continue
				}
				lastWrites, lastSecond = writes, second
				output, _ := capture.String()
				if publish(output, elapsed) != nil {
					return
				}
			}
		}
	}()

	return func() {
		close(stop)
		<-stopped
	}
}
