package opencodego

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/compaction"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type protocolClient interface {
	Generate(context.Context, agent.Request, agent.StreamObserver) (agent.Response, error)
	SemanticCompact(context.Context, agent.Request) (agent.CompactResponse, error)
}

type provider struct {
	models  *backend.Models[modelInfo]
	clients map[protocol]protocolClient
}

func newProvider(apiKey, baseURL string, httpClient *http.Client, models *backend.Models[modelInfo]) *provider {
	return &provider{models: models, clients: newProtocolClients(apiKey, baseURL, httpClient, models)}
}

func (provider *provider) modelClient(model string) (modelInfo, protocolClient, error) {
	info, err := provider.models.Lookup(model)
	if err != nil {
		return modelInfo{}, nil, err
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
	info, _, err := provider.modelClient(request.Model)
	if err != nil {
		return false
	}
	return compaction.ShouldCompact(request, usage, info.contextWindow)
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
