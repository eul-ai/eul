package responses

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type responseReadErrorFunc func(context.Context, error, string) error

var generationRetryPolicy = backendhttp.RetryPolicy{
	MaximumAttempts: 20,
	BaseDelay:       500 * time.Millisecond,
	MaximumDelay:    5 * time.Minute,
}

type responseFailureError struct {
	message string
	detail  responseError
}

func (e *responseFailureError) Error() string { return e.message }

func (c *Client) generationReadError(ctx context.Context, err error, _ string) error {
	if backendhttp.IsNonRetryableStreamError(err) {
		return c.wrapf(err, "%v", err)
	}

	classified := backendhttp.ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return classified.Cause
	}
	if classified.Retryable {
		return c.retryableWrapf(classified.Cause, "%v", err)
	}
	return c.wrapf(classified.Cause, "%v", err)
}

func (c *Client) compactionReadError(ctx context.Context, err error, operation string) error {
	classified := backendhttp.ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return classified.Cause
	}
	switch {
	case errors.Is(classified.Cause, context.DeadlineExceeded):
		return c.retryableWrapf(context.DeadlineExceeded, "read %s: %v", operation, err)
	case errors.Is(classified.Cause, context.Canceled):
		return c.wrapf(context.Canceled, "read %s: %v", operation, err)
	default:
		return c.errorf("read %s: %v", operation, err)
	}
}

func (c *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return generationRetryPolicy.Next(err, failedAttempts, retryableGenerationError(err))
}

func retryableGenerationError(err error) bool {
	if contextLimitError(err) || backendhttp.IsNonRetryableStreamError(err) {
		return false
	}

	var httpErr *backendhttp.APIResponseError
	if errors.As(err, &httpErr) {
		return backendhttp.RetryableHTTPStatus(httpErr.StatusCode())
	}

	var responseErr *responseFailureError
	if errors.As(err, &responseErr) {
		return retryableResponseError(responseErr.detail)
	}

	return backendhttp.IsRetryableOperation(err) || errors.Is(err, errResponsesSSEIncomplete)
}

func (c *Client) IsContextLimitError(err error) bool {
	return contextLimitError(err)
}

func contextLimitError(err error) bool {
	var httpErr *backendhttp.APIResponseError
	if errors.As(err, &httpErr) && contextLimitResponseError(responseErrorFromAPI(httpErr.Detail())) {
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

var _ agent.GenerationRetryPolicy = (*Client)(nil)
