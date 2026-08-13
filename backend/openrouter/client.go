package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
)

type client struct {
	responses *responses.Client
}

var (
	_ agent.Provider              = (*client)(nil)
	_ agent.GenerationRetryPolicy = (*client)(nil)
)

func newClient(apiKey, endpoint string, httpClient *http.Client, supportsReasoning func(string) bool) (*client, error) {
	shared, err := responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       endpoint,
		ErrorPrefix:    "openrouter",
		PrepareRequest: prepareRequest(apiKey),
		RequestOptions: requestOptions(supportsReasoning),
		Redact:         []string{apiKey},
	})
	if err != nil {
		return nil, err
	}
	return &client{responses: shared}, nil
}

func (c *client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
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

func requestOptions(supportsReasoning func(string) bool) responses.RequestOptionsFunc {
	return func(request agent.Request) (responses.RequestOptions, error) {
		level := request.ThinkingLevel
		if level == "" {
			level = agent.DefaultThinkingLevel
		}

		efforts := map[agent.ThinkingLevel]string{
			agent.ThinkingOff:     "none",
			agent.ThinkingMinimal: "minimal",
			agent.ThinkingLow:     "low",
			agent.ThinkingMedium:  "medium",
			agent.ThinkingHigh:    "high",
			agent.ThinkingXHigh:   "xhigh",
		}
		effort, valid := efforts[level]
		if !valid || level != agent.ThinkingOff && !supportsReasoning(request.Model) {
			return responses.RequestOptions{}, &unsupportedThinkingLevelError{level: level, model: request.Model}
		}

		options := responses.RequestOptions{ToolChoice: "auto", ParallelToolCalls: true}
		if supportsReasoning(request.Model) {
			options.Reasoning = &responses.Reasoning{Effort: effort}
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
