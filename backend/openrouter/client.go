package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/compaction"
	"github.com/eul-ai/eul/backend/openai/responses"
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

func newClient(apiKey, endpoint string, httpClient *http.Client, metadata func(string) modelMetadata) (*client, error) {
	shared, err := responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       endpoint,
		ErrorPrefix:    "openrouter",
		PrepareRequest: prepareRequest(apiKey),
		RequestOptions: requestOptions(metadata),
	})
	if err != nil {
		return nil, err
	}
	return &client{responses: shared, metadata: metadata}, nil
}

func (c *client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
}

func (c *client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	return compaction.ShouldCompact(
		request,
		usage,
		c.metadata(request.Model).contextWindow,
		c.responses.ShouldCompactState(request),
	)
}

func (c *client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return c.responses.SemanticCompact(ctx, request, compaction.Instructions)
}

func (c *client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return c.responses.IsContextLimitError(err)
}

func (c *client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return c.responses.RetryGeneration(err, failedAttempts)
}

func prepareRequest(apiKey string) responses.PrepareRequestFunc {
	return func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("HTTP-Referer", "https://github.com/eul-ai/eul")
		request.Header.Set("X-Title", "Eul")
		request.Header.Set("User-Agent", "eul")
		return nil
	}
}

func requestOptions(metadataFor func(string) modelMetadata) responses.RequestOptionsFunc {
	return func(request agent.Request) (responses.RequestOptions, error) {
		metadata := metadataFor(request.Model)
		level := request.ThinkingLevel
		if level == "" {
			level = metadata.defaultThinkingLevel
			if level == "" && !metadata.reasoning {
				level = agent.ThinkingOff
			}
		}

		supported := !metadata.reasoning && level == agent.ThinkingOff
		for _, candidate := range metadata.thinkingLevels {
			if candidate == level {
				supported = true
				break
			}
		}
		if !supported {
			return responses.RequestOptions{}, &unsupportedThinkingLevelError{level: level, model: request.Model}
		}

		options := responses.RequestOptions{SessionID: request.SessionID, ToolChoice: "auto", ParallelToolCalls: true}
		if metadata.reasoning {
			effort := string(level)
			if level == agent.ThinkingOff {
				effort = "none"
			}
			options.Reasoning = &responses.Reasoning{Effort: effort}
			options.Include = []string{"reasoning.encrypted_content"}
		}
		return options, nil
	}
}

type unsupportedThinkingLevelError struct {
	level agent.ThinkingLevel
	model string
}

func (err *unsupportedThinkingLevelError) Error() string {
	return fmt.Sprintf("thinking level %q is not supported by model %q", err.level, err.model)
}
