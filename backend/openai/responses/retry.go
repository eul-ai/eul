package responses

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	maximumGenerationAttempts = 20
	generationRetryBaseDelay  = 500 * time.Millisecond
	generationRetryMaxDelay   = 5 * time.Minute
)

type retryableOperationError struct {
	cause error
}

func (e *retryableOperationError) Error() string { return e.cause.Error() }
func (e *retryableOperationError) Unwrap() error { return e.cause }

type httpResponseError struct {
	message    string
	statusCode int
	retryAfter time.Duration
	detail     responseError
	cause      error
}

func (e *httpResponseError) Error() string { return e.message }
func (e *httpResponseError) Unwrap() error { return e.cause }

func (c *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	if failedAttempts >= maximumGenerationAttempts || !retryableGenerationError(err) {
		return 0, false
	}

	delay := backendhttp.RetryDelay(failedAttempts, generationRetryBaseDelay, generationRetryMaxDelay)
	var responseErr *httpResponseError
	if errors.As(err, &responseErr) && responseErr.retryAfter > delay {
		delay = min(responseErr.retryAfter, generationRetryMaxDelay)
	}
	return delay, true
}

func retryableGenerationError(err error) bool {
	if contextLimitError(err) {
		return false
	}

	var observerErr *observerDeliveryError
	if errors.As(err, &observerErr) {
		return false
	}
	var partialErr *partialResponseError
	if errors.As(err, &partialErr) {
		return false
	}

	var httpErr *httpResponseError
	if errors.As(err, &httpErr) {
		return backendhttp.RetryableHTTPStatus(httpErr.statusCode)
	}

	var responseErr *responseFailureError
	if errors.As(err, &responseErr) {
		return retryableResponseError(responseErr.detail)
	}

	var operationErr *retryableOperationError
	return errors.As(err, &operationErr) || errors.Is(err, errResponsesSSEIncomplete)
}

func (c *Client) IsContextLimitError(err error) bool {
	return contextLimitError(err)
}

func contextLimitError(err error) bool {
	var httpErr *httpResponseError
	if errors.As(err, &httpErr) && contextLimitResponseError(httpErr.detail) {
		return true
	}

	var responseErr *responseFailureError
	return errors.As(err, &responseErr) && contextLimitResponseError(responseErr.detail)
}

func contextLimitResponseError(detail responseError) bool {
	return strings.EqualFold(string(detail.Code), "context_length_exceeded") ||
		strings.EqualFold(detail.Type, "context_length_exceeded")
}

func retryableResponseError(detail responseError) bool {
	if code, err := strconv.Atoi(string(detail.Code)); err == nil && backendhttp.RetryableHTTPStatus(code) {
		return true
	}
	for _, value := range []string{detail.Type, string(detail.Code)} {
		switch strings.ToLower(value) {
		case "server_error", "service_unavailable_error", "server_is_overloaded", "rate_limit", "rate_limit_error", "rate_limit_exceeded":
			return true
		}
	}
	return false
}

func (c *Client) retryableWrapf(cause error, format string, arguments ...any) error {
	return &retryableOperationError{cause: c.wrapf(cause, format, arguments...)}
}

var _ agent.GenerationRetryPolicy = (*Client)(nil)
