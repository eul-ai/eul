package tool

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
)

func setFinalBashPresentation(updates agent.ToolUpdateSink, command, output, outcome string, elapsed time.Duration) {
	if updates != nil {
		updates.SetFinal(bashOutputPresentation(command, output, outcome, elapsed))
	}
}

func bashOutputPresentation(command, output, outcome string, elapsed time.Duration) agent.ToolPresentation {
	presentation := bashPresentation(command)
	presentation.Outcome = outcome
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		presentation.Lines = strings.Split(trimmed, "\n")
	}
	presentation.TailLines = bashPreviewLines
	presentation.Elapsed = max(time.Nanosecond, elapsed)
	return presentation
}

type bashOutputStreamer struct {
	capture  *tailCapture
	updates  agent.ToolUpdateSink
	command  string
	started  time.Time
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
	cancel context.CancelFunc,
) *bashOutputStreamer {
	return &bashOutputStreamer{
		capture: capture,
		updates: updates,
		command: command,
		started: started,
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
			if err := s.updates.Update(bashOutputPresentation(s.command, output, "", elapsed)); err != nil {
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
