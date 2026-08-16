package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/openai/responses"
)

const (
	defaultBaseURL               = "https://chatgpt.com/backend-api"
	defaultHTTPTimeout           = 10 * time.Minute
	defaultMaxUsageResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxUsageErrorBytes    = int64(64 * 1024)
)

type Options struct {
	HTTPClient       *http.Client
	BaseURL          string
	ReasoningSummary ReasoningSummary
}

type Credential struct {
	AccessToken string
	AccountID   string
}

type TokenSource interface {
	Token(context.Context) (Credential, error)
}

type Client struct {
	httpClient            *http.Client
	usageEndpoint         string
	tokenSource           TokenSource
	responses             *responses.Client
	maxUsageResponseBytes int64
	maxUsageErrorBytes    int64
	reasoningSummary      ReasoningSummary
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
	if source == nil {
		return nil, errors.New("openai: token source is required")
	}

	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	httpClient := backendhttp.New(options.HTTPClient, defaultHTTPTimeout)

	baseURL = strings.TrimRight(baseURL, "/")
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: parse base URL: %w", err)
	}
	usageEndpoint := baseURL + "/api/codex/usage"
	if strings.HasSuffix(strings.TrimRight(parsedBaseURL.Path, "/"), "/backend-api") {
		usageEndpoint = baseURL + "/wham/usage"
	}

	client := &Client{
		httpClient:            httpClient,
		usageEndpoint:         usageEndpoint,
		tokenSource:           source,
		maxUsageResponseBytes: defaultMaxUsageResponseBytes,
		maxUsageErrorBytes:    defaultMaxUsageErrorBytes,
		reasoningSummary:      reasoningSummary,
	}
	client.responses, err = responses.New(responses.Options{
		HTTPClient:     httpClient,
		Endpoint:       baseURL + "/codex/responses",
		ErrorPrefix:    "openai",
		PrepareRequest: client.prepareResponsesRequest,
		RequestOptions: client.responsesRequestOptions,
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
	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return err
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	return nil
}

func setCredentialHeaders(request *http.Request, credential Credential) {
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
}

func (c *Client) get(ctx context.Context, endpoint string, credential Credential, operation string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, c.wrapf(err, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _, readErr := backendhttp.ReadBounded(response.Body, c.maxUsageErrorBytes)
		if readErr != nil {
			return nil, c.wrapf(readErr, "HTTP %s; read error response: %v", response.Status, readErr)
		}
		return nil, c.errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func (c *Client) contextError(ctx context.Context, err error, operation string) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return c.wrapf(context.DeadlineExceeded, "%s: %v", operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return c.wrapf(context.Canceled, "%s: %v", operation, err)
	}
	return nil
}

func (c *Client) resolveCredential(ctx context.Context) (Credential, error) {
	credential, err := c.tokenSource.Token(ctx)
	if err == nil {
		return credential, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return Credential{}, contextErr
	}
	return Credential{}, c.wrapf(err, "resolve authentication: %v", err)
}

func (c *Client) errorf(format string, arguments ...any) error {
	return errors.New(c.errorMessage(format, arguments...))
}

func (c *Client) wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: c.errorMessage(format, arguments...), cause: cause}
}

func (c *Client) errorMessage(format string, arguments ...any) string {
	message := strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�")
	return backendhttp.TruncateUTF8("openai: "+message, int(c.maxUsageErrorBytes))
}

type wrappedError struct {
	message string
	cause   error
}

func (e *wrappedError) Error() string { return e.message }
func (e *wrappedError) Unwrap() error { return e.cause }
