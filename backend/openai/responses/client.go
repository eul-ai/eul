package responses

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
	defaultHTTPTimeout         = 10 * time.Minute
	defaultMaxRequestBytes     = int64(32 * 1024 * 1024)
	defaultMaxResponseBytes    = int64(16 * 1024 * 1024)
	defaultMaxErrorBytes       = int64(64 * 1024)
	defaultMaxStateBytes       = 16 * 1024 * 1024
	defaultStateOutputHeadroom = 1024 * 1024
)

type RequestOptions struct {
	Reasoning         *Reasoning
	ServiceTier       string
	TextVerbosity     string
	Include           []string
	ToolChoice        string
	ParallelToolCalls bool
}

type RequestOptionsFunc func(agent.Request) (RequestOptions, error)

type PrepareRequestFunc func(context.Context, *http.Request) error

type Options struct {
	HTTPClient          *http.Client
	Endpoint            string
	ErrorPrefix         string
	PrepareRequest      PrepareRequestFunc
	RequestOptions      RequestOptionsFunc
	MaxRequestBytes     int64
	MaxResponseBytes    int64
	MaxErrorBytes       int64
	MaxStateBytes       int
	StateOutputHeadroom int
	Redact              []string
}

type Client struct {
	httpClient          *http.Client
	endpoint            string
	errorPrefix         string
	prepareRequest      PrepareRequestFunc
	requestOptions      RequestOptionsFunc
	maxRequestBytes     int64
	maxResponseBytes    int64
	maxErrorBytes       int64
	maxStateBytes       int
	stateOutputHeadroom int
	redact              []string
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.Endpoint) == "" {
		return nil, errors.New("responses: endpoint is required")
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

	maxRequestBytes := options.MaxRequestBytes
	if maxRequestBytes <= 0 {
		maxRequestBytes = defaultMaxRequestBytes
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxErrorBytes := options.MaxErrorBytes
	if maxErrorBytes <= 0 {
		maxErrorBytes = defaultMaxErrorBytes
	}
	maxStateBytes := options.MaxStateBytes
	if maxStateBytes <= 0 {
		maxStateBytes = defaultMaxStateBytes
	}
	stateOutputHeadroom := options.StateOutputHeadroom
	if stateOutputHeadroom <= 0 {
		stateOutputHeadroom = defaultStateOutputHeadroom
	}

	return &Client{
		httpClient:          httpClient,
		endpoint:            options.Endpoint,
		errorPrefix:         strings.TrimSpace(options.ErrorPrefix),
		prepareRequest:      options.PrepareRequest,
		requestOptions:      options.RequestOptions,
		maxRequestBytes:     maxRequestBytes,
		maxResponseBytes:    maxResponseBytes,
		maxErrorBytes:       maxErrorBytes,
		maxStateBytes:       maxStateBytes,
		stateOutputHeadroom: stateOutputHeadroom,
		redact:              append([]string(nil), options.Redact...),
	}, nil
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

func (c *Client) ShouldCompactState(request agent.Request) bool {
	if len(request.State) == 0 {
		return false
	}
	if _, _, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes()); err == nil {
		return false
	}

	withoutState := request
	withoutState.State = nil
	_, _, err := buildCreateRequestWithLimit(withoutState, c.maxStateBytes, c.generationStateBytes())
	return err == nil
}

func (c *Client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	wireRequest, newInputs, err := buildCreateRequestWithLimit(request, c.maxStateBytes, c.generationStateBytes())
	if err != nil {
		return agent.Response{}, c.errorf("build request: %v", err)
	}
	if err := c.configureRequest(request, &wireRequest); err != nil {
		return agent.Response{}, c.errorf("%v", err)
	}

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.Response{}, c.errorf("encode request: %v", err)
	}
	if oversized {
		return agent.Response{}, c.errorf("request exceeds %d bytes", c.maxRequestBytes)
	}

	httpResponse, err := c.post(ctx, requestBody, "request")
	if err != nil {
		return agent.Response{}, err
	}
	defer httpResponse.Body.Close()

	stream := streamObserver{observer: observer}
	wireResponse, err := readResponsesSSE(httpResponse.Body, c.maxResponseBytes, &stream)
	if err != nil {
		c.redactResponseFailure(err)
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
		c.redactResponseFailure(err)
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

	return agent.Response{Text: text, ToolCalls: calls, State: state, Usage: usage}, nil
}

func (c *Client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.CompactResponse{}, err
	}

	wireRequest, err := buildCompactRequest(request, c.maxStateBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("build compact request: %v", err)
	}
	if err := c.configureRequest(request, &wireRequest); err != nil {
		return agent.CompactResponse{}, c.errorf("%v", err)
	}

	requestBody, oversized, err := marshalBoundedJSON(wireRequest, c.maxRequestBytes)
	if err != nil {
		return agent.CompactResponse{}, c.errorf("encode compact request: %v", err)
	}
	if oversized {
		return agent.CompactResponse{}, c.errorf("compact request exceeds %d bytes", c.maxRequestBytes)
	}

	httpResponse, err := c.post(ctx, requestBody, "compact request")
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
		c.redactResponseFailure(err)
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

func (c *Client) configureRequest(request agent.Request, wireRequest *createResponseRequest) error {
	options := RequestOptions{}
	if c.requestOptions != nil {
		var err error
		options, err = c.requestOptions(request)
		if err != nil {
			return err
		}
	}

	wireRequest.ServiceTier = options.ServiceTier
	wireRequest.Stream = true
	wireRequest.Reasoning = options.Reasoning
	wireRequest.Include = append([]string(nil), options.Include...)
	if options.TextVerbosity != "" {
		wireRequest.Text = &responseText{Verbosity: options.TextVerbosity}
	}
	wireRequest.ToolChoice = options.ToolChoice
	wireRequest.ParallelToolCalls = options.ParallelToolCalls
	return nil
}

func marshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	return body, int64(len(body)) > maximum, nil
}

func (c *Client) post(ctx context.Context, body []byte, operation string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if c.prepareRequest != nil {
		if err := c.prepareRequest(ctx, request); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
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
	detail.Message = c.redactMessage(detail.Message)
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
	message := c.redactMessage(strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�"))
	if c.errorPrefix != "" {
		message = c.errorPrefix + ": " + message
	}
	return truncateUTF8(message, int(c.maxErrorBytes))
}

func (c *Client) redactMessage(message string) string {
	for _, value := range c.redact {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}

func (c *Client) redactResponseFailure(err error) {
	var responseErr *responseFailureError
	if !errors.As(err, &responseErr) {
		return
	}
	responseErr.message = c.redactMessage(responseErr.message)
	responseErr.detail.Message = c.redactMessage(responseErr.detail.Message)
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
