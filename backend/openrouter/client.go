package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/protocol"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

type client struct {
	responses *responses.Client
	metadata  func(string) modelMetadata
}

var (
	_ agent.Provider              = (*client)(nil)
	_ agent.GenerationRetryPolicy = (*client)(nil)
	_ agent.Compactor             = (*client)(nil)
	_ agent.CompactionErrorPolicy = (*client)(nil)
)

func newClient(apiKey, endpoint string, httpClient *http.Client, metadata func(string) modelMetadata) *client {
	return &client{
		responses: responses.New(responses.Options{
			HTTPClient:     httpClient,
			Endpoint:       endpoint,
			ErrorPrefix:    "openrouter",
			PrepareRequest: prepareRequest(apiKey),
			RequestOptions: requestOptions(metadata),
		}),
		metadata: metadata,
	}
}

func (c *client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
}

func (c *client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	return protocol.ShouldCompact(request, usage, c.metadata(request.Model).contextWindow)
}

func (c *client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return c.responses.SemanticCompact(ctx, request)
}

func (*client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return backendhttp.IsContextLimit(err)
}

func (*client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
}

func prepareRequest(apiKey string) responses.PrepareRequestFunc {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		setCommonHeaders(request)
		return nil
	}
}

func setCommonHeaders(request *http.Request) {
	request.Header.Set("HTTP-Referer", "https://github.com/eul-ai/eul")
	request.Header.Set("X-Title", "Eul")
	request.Header.Set("User-Agent", "eul")
}

func requestOptions(metadataFor func(string) modelMetadata) responses.RequestOptionsFunc {
	return func(request agent.Request) (responses.RequestOptions, error) {
		options := responses.RequestOptions{SessionID: request.SessionID, ToolChoice: "auto", ParallelToolCalls: true}
		efforts := metadataFor(request.Model).efforts
		effort, supported := efforts[request.ThinkingLevel]
		switch {
		case len(efforts) == 0 && request.ThinkingLevel == agent.ThinkingOff:
			return options, nil
		case !supported:
			return responses.RequestOptions{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
		}

		options.Reasoning = &responses.Reasoning{Effort: effort}
		options.Include = []string{"reasoning.encrypted_content"}
		return options, nil
	}
}
