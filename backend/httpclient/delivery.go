package httpclient

import (
	"errors"

	"github.com/eul-ai/eul/agent"
)

type observerError struct {
	operation string
	cause     error
}

func (err *observerError) Error() string { return err.operation + ": " + err.cause.Error() }
func (err *observerError) Unwrap() error { return err.cause }

func isObserverError(err error) bool {
	var observerErr *observerError
	return errors.As(err, &observerErr)
}

type Observer struct {
	sinks agent.StreamObserver
}

func NewObserver(sinks agent.StreamObserver) Observer {
	return Observer{sinks: sinks}
}

func (observer Observer) Text(text string) error {
	if text == "" || observer.sinks.Text == nil {
		return nil
	}
	return deliver("deliver text", observer.sinks.Text(text))
}

func (observer Observer) Reasoning(text string) error {
	if text == "" || observer.sinks.Reasoning == nil {
		return nil
	}
	return deliver("deliver reasoning", observer.sinks.Reasoning(text))
}

func (observer Observer) ToolCall(id, name, arguments string, complete bool) error {
	if observer.sinks.ToolCall == nil {
		return nil
	}
	return deliver("deliver tool call", observer.sinks.ToolCall(agent.ToolCallSnapshot{
		ID:           id,
		Name:         name,
		RawArguments: arguments,
		Complete:     complete,
	}))
}

func deliver(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &observerError{operation: operation, cause: err}
}
