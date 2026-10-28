package opencodego

import (
	"context"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/protocol"
)

type provider struct {
	models  *backend.Models[modelInfo]
	clients map[wireProtocol]*protocol.Client
}

func newProvider(apiKey, baseURL string, httpClient *http.Client, models *backend.Models[modelInfo]) *provider {
	return &provider{models: models, clients: newProtocolClients(apiKey, baseURL, httpClient, models)}
}

func (provider *provider) client(model string) (*protocol.Client, error) {
	info, err := provider.models.Lookup(model)
	if err != nil {
		return nil, err
	}
	return provider.clients[info.protocol], nil
}

func (provider *provider) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	client, err := provider.client(request.Model)
	if err != nil {
		return agent.Response{}, err
	}
	return client.Generate(ctx, request, observer)
}

func (provider *provider) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	client, err := provider.client(request.Model)
	return err == nil && client.ShouldCompact(request, usage)
}

func (provider *provider) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	client, err := provider.client(request.Model)
	if err != nil {
		return agent.CompactResponse{}, err
	}
	return client.Compact(ctx, request)
}

func (*provider) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
}

func (*provider) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return backendhttp.IsContextLimit(err)
}
