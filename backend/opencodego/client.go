package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type protocolClient interface {
	Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error)
	ShouldCompactState(agent.Request) bool
	SemanticCompact(context.Context, agent.Request) (agent.CompactResponse, error)
}

type provider struct {
	models  map[string]modelInfo
	clients map[protocol]protocolClient
}

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
	return selected.Generate(ctx, request, observer)
}

func (*provider) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
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
	return selected.SemanticCompact(ctx, request)
}

func (*provider) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return backendhttp.IsContextLimit(err)
}

var (
	_ agent.Provider              = (*provider)(nil)
	_ agent.GenerationRetryPolicy = (*provider)(nil)
	_ agent.Compactor             = (*provider)(nil)
	_ agent.CompactionErrorPolicy = (*provider)(nil)
)
