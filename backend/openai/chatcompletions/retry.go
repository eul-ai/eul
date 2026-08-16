package chatcompletions

import (
	"errors"
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

var generationRetryPolicy = backendhttp.RetryPolicy{
	MaximumAttempts: 20,
	BaseDelay:       500 * time.Millisecond,
	MaximumDelay:    5 * time.Minute,
}

func (client *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return generationRetryPolicy.Next(err, failedAttempts, retryableGenerationError(err))
}

func retryableGenerationError(err error) bool {
	if contextLimitError(err) {
		return false
	}

	if backendhttp.IsNonRetryableStreamError(err) {
		return false
	}

	var httpErr *backendhttp.APIResponseError
	if errors.As(err, &httpErr) {
		return backendhttp.RetryableHTTPStatus(httpErr.StatusCode())
	}
	var responseErr *responseFailureError
	if errors.As(err, &responseErr) {
		return backendhttp.IsRetryableAPIError(responseErr.detail)
	}
	return backendhttp.IsRetryableOperation(err) || errors.Is(err, errSSEIncomplete)
}

func (client *Client) IsContextLimitError(err error) bool {
	return contextLimitError(err)
}

func contextLimitError(err error) bool {
	var httpErr *backendhttp.APIResponseError
	if errors.As(err, &httpErr) && backendhttp.IsContextLimitAPIError(httpErr.Detail()) {
		return true
	}
	var responseErr *responseFailureError
	return errors.As(err, &responseErr) && backendhttp.IsContextLimitAPIError(responseErr.detail)
}

var (
	_ agent.Provider              = (*Client)(nil)
	_ agent.GenerationRetryPolicy = (*Client)(nil)
)
