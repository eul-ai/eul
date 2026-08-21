package opencodego

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
)

type protocolClient interface {
	Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error)
	RetryGeneration(error, int) (time.Duration, bool)
	ShouldCompactState(agent.Request) bool
	SemanticCompact(context.Context, agent.Request, string) (agent.CompactResponse, error)
	IsContextLimitError(error) bool
}

type provider struct {
	models  map[string]modelInfo
	clients map[protocol]protocolClient
}

type routedError struct {
	client protocolClient
	cause  error
}

func (err *routedError) Error() string { return err.cause.Error() }
func (err *routedError) Unwrap() error { return err.cause }

func newProvider(apiKey, baseURL string, httpClient *http.Client, models map[string]modelInfo) (*provider, error) {
	clients, err := newProtocolClients(apiKey, baseURL, httpClient, models)
	if err != nil {
		return nil, err
	}
	return &provider{models: models, clients: clients}, nil
}

func (provider *provider) modelClient(model string) (modelInfo, protocolClient, error) {
	info, ok := provider.models[model]
	if !ok {
		return modelInfo{}, nil, modelNotSupportedError(model, provider.models)
	}
	selected, ok := provider.clients[info.protocol]
	if !ok {
		return modelInfo{}, nil, fmt.Errorf("opencode go: model %q has no protocol", model)
	}
	return info, selected, nil
}

func (provider *provider) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	_, selected, err := provider.modelClient(request.Model)
	if err != nil {
		return agent.Response{}, err
	}
	response, err := selected.Generate(ctx, request, observer)
	if err != nil {
		return agent.Response{}, &routedError{client: selected, cause: err}
	}
	return response, nil
}

func (provider *provider) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	var routed *routedError
	if !errors.As(err, &routed) {
		return 0, false
	}
	return routed.client.RetryGeneration(routed.cause, failedAttempts)
}

func (provider *provider) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	info, selected, err := provider.modelClient(request.Model)
	if err != nil {
		return false
	}
	return compaction.ShouldCompact(
		request,
		usage,
		info.contextWindow,
		selected.ShouldCompactState(request),
	)
}

func (provider *provider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	_, selected, err := provider.modelClient(request.Model)
	if err != nil {
		return agent.CompactResponse{}, err
	}
	response, err := selected.SemanticCompact(ctx, request, compaction.Instructions)
	if err != nil {
		return agent.CompactResponse{}, &routedError{client: selected, cause: err}
	}
	return response, nil
}

func (provider *provider) ShouldCompactAfterError(request agent.Request, err error) bool {
	_, selected, lookupErr := provider.modelClient(request.Model)
	if lookupErr != nil {
		return false
	}
	var routed *routedError
	if !errors.As(err, &routed) || routed.client != selected {
		return false
	}
	return selected.IsContextLimitError(routed.cause)
}

var (
	_ agent.Provider              = (*provider)(nil)
	_ agent.GenerationRetryPolicy = (*provider)(nil)
	_ agent.Compactor             = (*provider)(nil)
	_ agent.CompactionErrorPolicy = (*provider)(nil)
)
