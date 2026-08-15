package chatcompletions

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

func (client *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
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
		return retryableAPIError(responseErr.detail)
	}

	var operationErr *retryableOperationError
	return errors.As(err, &operationErr) || errors.Is(err, errSSEIncomplete)
}

func (client *Client) IsContextLimitError(err error) bool {
	return contextLimitError(err)
}

func contextLimitError(err error) bool {
	var httpErr *httpResponseError
	if errors.As(err, &httpErr) && contextLimitAPIError(httpErr.detail) {
		return true
	}

	var responseErr *responseFailureError
	return errors.As(err, &responseErr) && contextLimitAPIError(responseErr.detail)
}

func contextLimitAPIError(detail apiError) bool {
	code := strings.ToLower(string(detail.Code))
	typeName := strings.ToLower(detail.Type)
	if code == "context_length_exceeded" || typeName == "context_length_exceeded" {
		return true
	}
	if typeName != "invalid_request_error" && code != "invalid_request_error" && code != "bad_request" {
		return false
	}
	message := strings.ToLower(detail.Message)
	return strings.Contains(message, "context length") ||
		strings.Contains(message, "maximum context") ||
		strings.Contains(message, "prompt is too long")
}

func retryableAPIError(detail apiError) bool {
	if code, err := strconv.Atoi(string(detail.Code)); err == nil && backendhttp.RetryableHTTPStatus(code) {
		return true
	}
	for _, value := range []string{detail.Type, string(detail.Code)} {
		switch strings.ToLower(value) {
		case "server_error", "service_unavailable_error", "server_is_overloaded", "rate_limit", "rate_limit_error", "rate_limit_exceeded", "api_error", "overloaded_error":
			return true
		}
	}
	return false
}

var (
	_ agent.Provider              = (*Client)(nil)
	_ agent.GenerationRetryPolicy = (*Client)(nil)
)
