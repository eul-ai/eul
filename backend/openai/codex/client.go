package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eul-ai/eul/agent"
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
	_ agent.Compactor             = (*Client)(nil)
	_ agent.CompactionErrorPolicy = (*Client)(nil)
	_ agent.UsageProvider         = (*Client)(nil)
	_ agent.ModelMetadataProvider = (*Client)(nil)
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
	endpoint := baseURL + "/codex/responses"
	usageEndpoint := baseURL + "/api/codex/usage"
	if strings.Contains(baseURL, "/backend-api") {
		usageEndpoint = baseURL + "/wham/usage"
	}

	return &Client{
		httpClient:          httpClient,
		endpoint:            endpoint,
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

func (*Client) ModelMetadata(model string) agent.ModelMetadata {
	return agent.ModelMetadata{
		ContextWindow:  contextWindow(model),
		ThinkingLevels: supportedThinkingLevels(model),
		FastMode:       models[model].fastMode,
	}
}

func (c *Client) stateOutputBudget() int {
	budget := c.stateOutputHeadroom
	if budget <= 0 {
		budget = min(defaultStateOutputHeadroom, c.maxStateBytes/4)
	}
	return min(budget, max(0, c.maxStateBytes-continuationStateEnvelopeBytes))
}

func (c *Client) generationStateBytes() int {
	return max(0, c.maxStateBytes-c.stateOutputBudget())
}

func (c *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	reasoning, err := responseReasoningFor(request.Model, request.ThinkingLevel, c.reasoningSummary)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	wireRequest, newInputs, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes())
	if err != nil {
		return agent.Response{}, c.errorf("build request: %v", err)
	}

	wireRequest.Reasoning = reasoning
	wireRequest.Stream = true
	wireRequest.Text = &responseText{Verbosity: "low"}
	wireRequest.ToolChoice = "auto"
	wireRequest.ParallelToolCalls = true

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.Response{}, c.errorf("encode request: %v", err)
	}
	if oversized {
		return agent.Response{}, c.errorf("request exceeds %d bytes", c.maxRequestBytes)
	}

	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return agent.Response{}, err
	}

	httpResponse, err := c.post(ctx, c.endpoint, "text/event-stream", requestBody, credential, "request")
	if err != nil {
		return agent.Response{}, err
	}
	defer httpResponse.Body.Close()

	stream := streamObserver{observer: observer}
	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, &stream)
	if err != nil {
		var observerErr *observerDeliveryError
		if errors.As(err, &observerErr) {
			return agent.Response{}, c.wrapf(err, "%v", err)
		}
		if classified := c.contextError(ctx, err, "read response"); classified != nil {
			return agent.Response{}, classified
		}
		if isRetryableNetworkError(err) {
			return agent.Response{}, c.retryableWrapf(err, "%v", err)
		}
		return agent.Response{}, c.wrapf(err, "%v", err)
	}

	text, calls, usage, err := normalizeResponse(wireResponse)
	if err != nil {
		return agent.Response{}, c.wrapf(err, "%v", err)
	}

	historyLength := len(wireRequest.Input) - len(newInputs)
	history := wireRequest.Input[:historyLength]
	outputStateBytes, err := encodedStateSize(wireResponse.Output)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}
	inputStateBytes, err := encodedStateSize(history, newInputs)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}
	if outputStateBytes-continuationStateEnvelopeBytes > c.maxStateBytes-inputStateBytes {
		return agent.Response{}, c.errorf("response output cannot fit continuation state")
	}
	state, err := encodeState(history, newInputs, wireResponse.Output, c.maxStateBytes)
	if err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	if text != "" && observer.Text != nil && !stream.sawDelta {
		if err := observer.Text(text); err != nil {
			deliveryErr := &observerDeliveryError{operation: "deliver text", cause: err}
			return agent.Response{}, c.wrapf(deliveryErr, "%v", deliveryErr)
		}
	}

	return agent.Response{
		Text:      text,
		ToolCalls: calls,
		State:     state,
		Usage:     usage,
	}, nil
}

func (c *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	reasoning, err := responseReasoningFor(request.Model, request.ThinkingLevel, c.reasoningSummary)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	wireRequest, err := buildCompactRequest(request, c.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("build compact request: %v", err)
	}
	wireRequest.Reasoning = reasoning
	wireRequest.Stream = true
	wireRequest.Text = &responseText{Verbosity: "low"}
	wireRequest.ToolChoice = "auto"
	wireRequest.ParallelToolCalls = true

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode compact request: %v", err)
	}
	if oversized {
		return agent.CompactResponse{}, c.errorf("compact request exceeds %d bytes", c.maxRequestBytes)
	}

	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return agent.CompactResponse{}, err
	}

	httpResponse, err := c.post(ctx, c.endpoint, "text/event-stream", requestBody, credential, "compact request")
	if err != nil {
		return agent.CompactResponse{}, err
	}
	defer httpResponse.Body.Close()

	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, nil)
	if err != nil {
		if classified := c.contextError(ctx, err, "read compact response"); classified != nil {
			return agent.CompactResponse{}, classified
		}
		return agent.CompactResponse{}, c.errorf("read compact response: %v", err)
	}
	if err := validateCompletedResponse(wireResponse); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	usage, err := normalizeUsage(wireResponse.Usage)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	if err := validateCompactOutput(wireResponse.Output); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}
	input := wireRequest.Input[:len(wireRequest.Input)-1]
	state, err := encodeState(nil, nil, compactedStateItems(input, wireResponse.Output), c.generationStateBytes())
	if err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	return agent.CompactResponse{State: state, Usage: usage}, nil
}

func marshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	return body, int64(len(body)) > maximum, nil
}

func (c *Client) post(ctx context.Context, endpoint, accept string, body []byte, credential Credential, operation string) (*http.Response, error) {
	return c.do(ctx, http.MethodPost, endpoint, accept, body, credential, operation)
}

func (c *Client) get(ctx context.Context, endpoint string, credential Credential, operation string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, endpoint, "application/json", nil, credential, operation)
}

func (c *Client) do(ctx context.Context, method, endpoint, accept string, body []byte, credential Credential, operation string) (*http.Response, error) {
	request, err := c.newRequest(ctx, method, endpoint, accept, body, credential)
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		if classified := c.contextError(ctx, err, operation+" failed"); classified != nil {
			return nil, classified
		}
		if isRetryableNetworkError(err) {
			return nil, c.retryableWrapf(err, "%s failed: %v", operation, err)
		}
		return nil, c.wrapf(err, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, c.decodeHTTPError(response)
	}
	return response, nil
}

func (c *Client) contextError(ctx context.Context, err error, operation string) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return c.retryableWrapf(context.DeadlineExceeded, "%s: %v", operation, err)
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

func (c *Client) newRequest(ctx context.Context, method, endpoint, accept string, body []byte, credential Credential) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", accept)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("OpenAI-Beta", "responses=experimental")
	}
	return request, nil
}

func (c *Client) decodeHTTPError(response *http.Response) error {
	body, truncated, err := readBounded(response.Body, c.maxErrorBytes)
	if err != nil {
		cause := err
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			cause = context.DeadlineExceeded
		case errors.Is(err, context.Canceled):
			cause = context.Canceled
		}
		return c.newHTTPResponseError(response, cause, responseError{}, "HTTP %s; read error response: %v", response.Status, err)
	}

	detail := strings.TrimSpace(string(body))
	var errorDetail responseError
	if !truncated {
		var envelope struct {
			Error responseError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if formatted := formatResponseError(envelope.Error); formatted != "" {
				errorDetail = envelope.Error
				detail = formatted
			}
		}
	}
	if detail == "" {
		detail = "empty error response"
	}
	if truncated {
		detail += " [truncated]"
	}

	return c.newHTTPResponseError(response, nil, errorDetail, "HTTP %s: %s", response.Status, detail)
}

func (c *Client) newHTTPResponseError(response *http.Response, cause error, detail responseError, format string, arguments ...any) error {
	return &httpResponseError{
		message:    c.errorMessage(format, arguments...),
		statusCode: response.StatusCode,
		retryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		detail:     detail,
		cause:      cause,
	}
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
