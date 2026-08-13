package app

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/terminal"
)

type terminalCallbacks struct {
	engine      *agent.Engine
	checkpoints *checkpointCoordinator
}

func newTerminalCallbacks(engine *agent.Engine, checkpoints *checkpointCoordinator) *terminalCallbacks {
	return &terminalCallbacks{engine: engine, checkpoints: checkpoints}
}

func (callbacks *terminalCallbacks) operations() terminal.Operations {
	return terminal.Operations{
		RunTurn: func(ctx context.Context, content []agent.ContentPart, stream terminal.EventStream) error {
			_, err := callbacks.engine.RunContent(ctx, content, callbacks.sink(stream))
			return err
		},
		Compact: func(ctx context.Context, stream terminal.EventStream) error {
			return callbacks.engine.Compact(ctx, callbacks.sink(stream))
		},
	}
}

func (callbacks *terminalCallbacks) sink(stream terminal.EventStream) agent.EventSink {
	return func(event agent.Event) error {
		if event.Kind != agent.EventCheckpoint || event.Checkpoint == nil || callbacks.checkpoints == nil {
			return stream.Emit(event)
		}

		terminalCheckpoint, err := stream.Snapshot()
		if err != nil {
			return err
		}
		return callbacks.checkpoints.SaveSessionCheckpoint(*event.Checkpoint, terminalCheckpoint, true)
	}
}
