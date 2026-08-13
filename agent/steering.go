package agent

import "context"

type steeringSignalKey struct{}

func SteeringSignal(ctx context.Context) <-chan struct{} {
	signal, _ := ctx.Value(steeringSignalKey{}).(<-chan struct{})
	return signal
}
