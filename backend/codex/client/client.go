package client

import (
	"context"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
)

type Options struct {
	HTTPClient       *http.Client
	BaseURL          string
	ReasoningSummary ReasoningSummary
}

type Client struct {
	requests         *requestClient
	responses        *responses.Client
	reasoningSummary ReasoningSummary
}

var (
	_ agent.Provider              = (*Client)(nil)
	_ agent.GenerationRetryPolicy = (*Client)(nil)
	_ agent.Compactor             = (*Client)(nil)
	_ agent.CompactionErrorPolicy = (*Client)(nil)
)

func New(source TokenSource, options Options) (*Client, error) {
	reasoningSummary, err := ParseReasoningSummary(string(options.ReasoningSummary))
	if err != nil {
		return nil, err
	}
	requests, err := newRequestClient(source, options.HTTPClient)
	if err != nil {
		return nil, err
	}
	baseURL, _, err := normalizeBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}

	client := &Client{requests: requests, reasoningSummary: reasoningSummary}
	client.responses, err = responses.New(responses.Options{
		HTTPClient:                requests.httpClient,
		Endpoint:                  baseURL + "/codex/responses",
		ErrorPrefix:               "openai",
		PrepareRequest:            client.prepareResponsesRequest,
		RequestOptions:            client.responsesRequestOptions,
		EncodeInboxAsAgentMessage: true,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
}

func (c *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return c.responses.Compact(ctx, request)
}

func (c *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return c.responses.RetryGeneration(err, failedAttempts)
}

func (c *Client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return c.responses.IsContextLimitError(err)
}

func (c *Client) responsesRequestOptions(request agent.Request) (responses.RequestOptions, error) {
	reasoning, err := responseReasoningFor(request.Model, request.ThinkingLevel, c.reasoningSummary)
	if err != nil {
		return responses.RequestOptions{}, err
	}

	serviceTier := ""
	if request.FastMode {
		serviceTier = "priority"
	}
	return responses.RequestOptions{
		Reasoning: &responses.Reasoning{
			Effort:  reasoning.Effort,
			Summary: reasoning.Summary,
		},
		ServiceTier:       serviceTier,
		TextVerbosity:     "low",
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}, nil
}

func (c *Client) prepareResponsesRequest(ctx context.Context, request *http.Request) error {
	if err := c.requests.authenticate(ctx, request); err != nil {
		return err
	}
	request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	return nil
}
