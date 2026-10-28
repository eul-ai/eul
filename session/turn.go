package session

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
)

type operation func(context.Context, agent.EventSink) error

// Send starts a turn with content, or queues it when a turn is running.
func (s *Session) Send(content []agent.ContentPart) error {
	if s.enqueue(content) {
		return nil
	}
	if err := s.saveActive(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript.AppendUser(content)
	s.startLocked(s.prompt(content))
	return nil
}

func (s *Session) Compact() error {
	if err := s.saveActive(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked(s.engine.Compact)
	return nil
}

// Interrupt cancels the running turn, denies the pending permission
// requests and returns the queued prompts.
func (s *Session) Interrupt() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.interrupted = true
		s.cancel()
	}
	s.answerLocked(len(s.permissions), false)
	return s.takeQueuedLocked()
}

// TakeQueued removes and returns the prompts the model has not received.
func (s *Session) TakeQueued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeQueuedLocked()
}

func (s *Session) Queued() [][]agent.ContentPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queue)
}

func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Session) enqueue(content []agent.ContentPart) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}

	if s.steered == len(s.queue) && !s.interrupted && s.engine.Steer(content) {
		s.steered++
	}
	s.queue = append(s.queue, content)
	return true
}

// takeQueuedLocked trusts the engine about the steered prompts it has not
// delivered. The others it already took from its queue, and the sink drops
// them from s.queue when their delivery events arrive.
func (s *Session) takeQueuedLocked() [][]agent.ContentPart {
	undelivered := s.engine.ClearSteering()
	delivering := s.steered - len(undelivered)
	taken := append(undelivered, s.queue[s.steered:]...)
	s.queue = s.queue[:delivering]
	s.steered = delivering
	return taken
}

func (s *Session) startLocked(run operation) {
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.interrupted, s.cancel = true, false, cancel
	s.workers.Go(func() { s.run(ctx, run) })
}

func (s *Session) run(ctx context.Context, run operation) {
	for {
		var ok bool
		if ctx, run, ok = s.settle(run(ctx, s.sink)); !ok {
			return
		}
	}
}

// settle finishes a turn. When it succeeded and prompts are queued, settle
// returns the next turn, which runs the first of them.
func (s *Session) settle(err error) (context.Context, operation, bool) {
	agentCheckpoint, checkpointErr := s.engine.Checkpoint()

	s.mu.Lock()
	s.cancel()
	s.transcript.FinishTurn(s.interrupted, err)
	s.steered = 0
	if !s.interrupted && err == nil && len(s.queue) > 0 {
		next := s.queue[0]
		s.queue = slices.Delete(s.queue, 0, 1)
		s.transcript.AppendUser(next)
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.mu.Unlock()

		s.publish(Update{Kind: UpdateEvent, Event: agent.Event{Kind: agent.EventSteering, Content: next}})
		return ctx, func(ctx context.Context, sink agent.EventSink) error {
			if err := s.saveActive(); err != nil {
				return err
			}
			return s.prompt(next)(ctx, sink)
		}, true
	}
	if s.interrupted {
		err = nil
	}
	s.running, s.interrupted = false, false
	s.mu.Unlock()

	if checkpointErr == nil {
		checkpointErr = s.save(agentCheckpoint, false)
	}
	s.publish(Update{Kind: UpdateSettled, Err: errors.Join(err, checkpointErr)})
	return nil, nil, false
}

func (s *Session) prompt(content []agent.ContentPart) operation {
	return func(ctx context.Context, sink agent.EventSink) error {
		content, err := expandSkill(content, s.skills)
		if err != nil {
			return err
		}
		_, err = s.engine.Run(ctx, content, sink)
		return err
	}
}

func (s *Session) sink(event agent.Event) error {
	if event.Kind == agent.EventCheckpoint {
		return s.save(*event.Checkpoint, true)
	}

	s.mu.Lock()
	s.transcript.Apply(event)
	if event.Kind == agent.EventSteering {
		s.queue = slices.Delete(s.queue, 0, 1)
		s.steered--
	}
	s.mu.Unlock()
	s.publish(Update{Kind: UpdateEvent, Event: event})
	return nil
}

func queuedInputs(queue [][]agent.ContentPart) []string {
	var inputs []string
	for _, content := range queue {
		if text := contentText(content); strings.TrimSpace(text) != "" {
			inputs = append(inputs, text)
		}
	}
	return inputs
}

func expandSkill(content []agent.ContentPart, skills []skill.Skill) ([]agent.ContentPart, error) {
	for index, part := range content {
		switch {
		case part.Kind == agent.ContentPartImage:
			return content, nil
		case strings.TrimSpace(part.Text) == "":
			continue
		}

		expanded, err := skill.Expand(part.Text, skills)
		if err != nil {
			return nil, err
		}
		content = slices.Clone(content)
		content[index].Text = expanded
		return content, nil
	}
	return content, nil
}
