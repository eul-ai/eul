package app

import (
	"context"
	"sync"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

func meterSession(runtime *backend.Runtime) (agent.Provider, func(context.Context) (backend.AccountUsage, error)) {
	if !runtime.ReportsCost {
		return runtime.Provider, runtime.Usage
	}
	cost := &sessionCost{}
	return meteredProvider{Provider: runtime.Provider, cost: cost}, cost.usage(runtime.Usage)
}

type sessionCost struct {
	mu    sync.Mutex
	total float64
}

func (cost *sessionCost) add(amount float64) {
	cost.mu.Lock()
	defer cost.mu.Unlock()
	cost.total += amount
}

func (cost *sessionCost) usage(load func(context.Context) (backend.AccountUsage, error)) func(context.Context) (backend.AccountUsage, error) {
	return func(ctx context.Context) (backend.AccountUsage, error) {
		usage, err := load(ctx)
		if err != nil {
			return usage, err
		}

		cost.mu.Lock()
		total := cost.total
		cost.mu.Unlock()
		usage.SessionUsageUSD = &total
		return usage, nil
	}
}

type meteredProvider struct {
	agent.Provider
	cost *sessionCost
}

func (provider meteredProvider) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	response, err := provider.Provider.Generate(ctx, request, observer)
	provider.cost.add(response.CostUSD)
	return response, err
}

func (provider meteredProvider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	response, err := provider.Provider.Compact(ctx, request)
	provider.cost.add(response.CostUSD)
	return response, err
}
