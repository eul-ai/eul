package codex

import (
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eul-ai/eul/agent"
)

const (
	maximumGenerationAttempts = 3
	generationRetryBaseDelay  = 500 * time.Millisecond
	generationRetryMaxDelay   = 8 * time.Second
)

type retryableOperationError struct {
	cause error
}

func (e *retryableOperationError) Error() string { return e.cause.Error() }
func (e *retryableOperationError) Unwrap() error { return e.cause }

// net/http exposes its internal HTTP/2 stream errors to matching structs through errors.As.
type http2StreamErrorCode uint32

type http2StreamError struct {
	StreamID uint32
	Code     http2StreamErrorCode
	Cause    error
}

func (http2StreamError) Error() string { return "HTTP/2 stream error" }

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

	delay := generationRetryDelay(failedAttempts)
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

	var httpErr *httpResponseError
	if errors.As(err, &httpErr) {
		return retryableHTTPStatus(httpErr.statusCode)
	}

	var responseErr *responseFailureError
	if errors.As(err, &responseErr) {
		return retryableResponseError(responseErr.detail)
	}

	var operationErr *retryableOperationError
	return errors.As(err, &operationErr) || errors.Is(err, errResponsesSSEIncomplete)
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError && status <= 599
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
	return strings.EqualFold(detail.Code, "context_length_exceeded")
}

func retryableResponseError(detail responseError) bool {
	for _, value := range []string{detail.Type, detail.Code} {
		switch strings.ToLower(value) {
		case "server_error", "service_unavailable_error", "server_is_overloaded", "rate_limit", "rate_limit_error", "rate_limit_exceeded":
			return true
		}
	}
	return false
}

func generationRetryDelay(failedAttempts int) time.Duration {
	delay := generationRetryBaseDelay
	for attempt := 1; attempt < failedAttempts && delay < generationRetryMaxDelay/2; attempt++ {
		delay *= 2
	}
	delay = min(delay, generationRetryMaxDelay)

	quarter := delay / 4
	if quarter == 0 {
		return delay
	}
	delay += time.Duration(rand.Int64N(int64(quarter)*2+1)) - quarter
	return min(delay, generationRetryMaxDelay)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if seconds > maximumSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}

	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func isRetryableNetworkError(err error) bool {
	const http2InternalError http2StreamErrorCode = 2

	var streamErr http2StreamError
	if errors.As(err, &streamErr) && streamErr.Code == http2InternalError {
		return true
	}

	for _, target := range []error{
		io.ErrUnexpectedEOF,
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, target) {
			return true
		}
	}

	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}

func (c *Client) retryableWrapf(cause error, format string, arguments ...any) error {
	return &retryableOperationError{cause: c.wrapf(cause, format, arguments...)}
}

var _ agent.GenerationRetryPolicy = (*Client)(nil)
