package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/openai/responses"
)

const (
	defaultBaseURL             = "https://chatgpt.com/backend-api"
	defaultHTTPTimeout         = 10 * time.Minute
	defaultMaxRequestBytes     = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes    = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes       = int64(64 * 1024)
	defaultMaxStateBytes       = 16 * 1024 * 1024
	defaultStateOutputHeadroom = 1024 * 1024
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
	httpClient          *http.Client
	endpoint            string
	usageEndpoint       string
	tokenSource         TokenSource
	maxRequestBytes     int64
	maxResponseBytes    int64
	maxErrorBytes       int64
	maxStateBytes       int
	stateOutputHeadroom int
	reasoningSummary    ReasoningSummary
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

	httpClient := &http.Client{}
	if options.HTTPClient != nil {
		*httpClient = *options.HTTPClient
	}
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = defaultHTTPTimeout
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	baseURL = strings.TrimRight(baseURL, "/")
	usageEndpoint := baseURL + "/api/codex/usage"
	if strings.Contains(baseURL, "/backend-api") {
		usageEndpoint = baseURL + "/wham/usage"
	}

	return &Client{
		httpClient:          httpClient,
		endpoint:            baseURL + "/codex/responses",
		usageEndpoint:       usageEndpoint,
		tokenSource:         source,
		maxRequestBytes:     defaultMaxRequestBytes,
		maxResponseBytes:    defaultMaxResponseBytes,
		maxErrorBytes:       defaultMaxErrorBytes,
		maxStateBytes:       defaultMaxStateBytes,
		stateOutputHeadroom: defaultStateOutputHeadroom,
		reasoningSummary:    reasoningSummary,
	}, nil
}

func (c *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	shared, err := c.responsesClient()
	if err != nil {
		return agent.Response{}, err
	}
	return shared.Generate(ctx, request, observer)
}

func (c *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	shared, err := c.responsesClient()
	if err != nil {
		return agent.CompactResponse{}, err
	}
	return shared.Compact(ctx, request)
}

func (c *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	shared, sharedErr := c.responsesClient()
	if sharedErr != nil {
		return 0, false
	}
	return shared.RetryGeneration(err, failedAttempts)
}

func (c *Client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	shared, sharedErr := c.responsesClient()
	return sharedErr == nil && shared.IsContextLimitError(err)
}

func (c *Client) responsesClient() (*responses.Client, error) {
	return responses.New(responses.Options{
		HTTPClient:          c.httpClient,
		Endpoint:            c.endpoint,
		ErrorPrefix:         "openai",
		PrepareRequest:      c.prepareResponsesRequest,
		RequestOptions:      c.responsesRequestOptions,
		MaxRequestBytes:     c.maxRequestBytes,
		MaxResponseBytes:    c.maxResponseBytes,
		MaxErrorBytes:       c.maxErrorBytes,
		MaxStateBytes:       c.maxStateBytes,
		StateOutputHeadroom: c.stateOutputHeadroom,
	})
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
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string, credential Credential, operation string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")

	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, c.wrapf(err, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _, readErr := readBounded(response.Body, c.maxErrorBytes)
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
	return truncateUTF8("openai: "+message, int(c.maxErrorBytes))
}

type wrappedError struct {
	message string
	cause   error
}

func (e *wrappedError) Error() string { return e.message }
func (e *wrappedError) Unwrap() error { return e.cause }

func readBounded(reader io.Reader, maximum int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= maximum {
		return data, false, nil
	}
	return data[:maximum], true, nil
}

func truncateUTF8(text string, maximum int) string {
	if maximum < 0 {
		maximum = 0
	}
	if len(text) <= maximum {
		return text
	}

	end := maximum
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
