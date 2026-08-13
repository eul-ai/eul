package interactive

import (
	"context"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal"
)

type terminalCallbacks struct {
	mu              sync.Mutex
	engine          *agent.Engine
	checkpoints     *checkpointCoordinator
	eventCheckpoint *agent.Checkpoint
	sessions        terminal.Sessions
	checkpointsPort terminal.Checkpoints
}

func newTerminalCallbacks(engine *agent.Engine) *terminalCallbacks {
	return &terminalCallbacks{engine: engine}
}

func (callbacks *terminalCallbacks) operations() terminal.Operations {
	return terminal.Operations{
		RunTurn: func(ctx context.Context, content []agent.ContentPart, sink agent.EventSink) error {
			_, err := callbacks.engine.RunContent(ctx, content, callbacks.captureCheckpoints(sink))
			return err
		},
		Compact: func(ctx context.Context, sink agent.EventSink) error {
			return callbacks.engine.Compact(ctx, callbacks.captureCheckpoints(sink))
		},
	}
}

func (callbacks *terminalCallbacks) captureCheckpoints(sink agent.EventSink) agent.EventSink {
	return func(event agent.Event) error {
		if event.Kind == agent.EventCheckpoint && event.Checkpoint != nil {
			checkpoint := *event.Checkpoint
			callbacks.mu.Lock()
			callbacks.eventCheckpoint = &checkpoint
			callbacks.mu.Unlock()
		}
		return sink(event)
	}
}

func (callbacks *terminalCallbacks) attachPersistence(
	checkpoints *checkpointCoordinator,
	listSessions func(context.Context) ([]terminal.SessionSummary, []string, error),
) {
	callbacks.mu.Lock()
	callbacks.checkpoints = checkpoints
	callbacks.mu.Unlock()
	callbacks.checkpointsPort.Save = callbacks.saveCheckpoint
	callbacks.sessions.List = listSessions
}

func (callbacks *terminalCallbacks) saveCheckpoint(terminalCheckpoint terminal.Checkpoint, active bool) error {
	callbacks.mu.Lock()
	coordinator := callbacks.checkpoints
	agentCheckpoint := callbacks.eventCheckpoint
	callbacks.eventCheckpoint = nil
	callbacks.mu.Unlock()
	if coordinator == nil {
		return nil
	}
	if agentCheckpoint == nil {
		checkpoint, err := callbacks.engine.Checkpoint()
		if err != nil {
			return err
		}
		agentCheckpoint = &checkpoint
	}
	return coordinator.SaveSessionCheckpoint(*agentCheckpoint, terminalCheckpoint, active)
}
