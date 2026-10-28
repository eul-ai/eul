package httpclient

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxGenerationAttempts = 20
	baseRetryDelay        = 500 * time.Millisecond
	maxRetryDelay         = 5 * time.Minute
)

var (
	ErrIncompleteStream = errors.New("stream ended before the response completed")
	ErrContextLimit     = errors.New("model context window exceeded")
)

type FailureError struct {
	message string
	detail  APIError
}

func (err *FailureError) Error() string { return err.message }

func NewFailureError(context string, detail APIError) error {
	text := FormatAPIError(detail)
	if text == "" {
		text = "unspecified error"
	}
	return &FailureError{message: context + ": " + text, detail: detail}
}

func RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	if failedAttempts >= maxGenerationAttempts || !retryableGeneration(err) {
		return 0, false
	}

	var hint time.Duration
	var responseErr *APIResponseError
	if errors.As(err, &responseErr) {
		hint = responseErr.retryAfter
	}
	return retryDelay(failedAttempts, hint), true
}

func retryableGeneration(err error) bool {
	if IsContextLimit(err) || IsObserverError(err) {
		return false
	}

	var responseErr *APIResponseError
	if errors.As(err, &responseErr) {
		return retryableHTTPStatus(responseErr.statusCode)
	}
	var failure *FailureError
	if errors.As(err, &failure) {
		return retryableAPIError(failure.detail)
	}
	var operation *retryableOperationError
	return errors.As(err, &operation) || errors.Is(err, ErrIncompleteStream)
}

func IsContextLimit(err error) bool {
	if errors.Is(err, ErrContextLimit) {
		return true
	}
	var responseErr *APIResponseError
	if errors.As(err, &responseErr) && contextLimitAPIError(responseErr.detail) {
		return true
	}
	var failure *FailureError
	return errors.As(err, &failure) && contextLimitAPIError(failure.detail)
}

func contextLimitAPIError(detail APIError) bool {
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

func retryableAPIError(detail APIError) bool {
	if code, err := strconv.Atoi(string(detail.Code)); err == nil && retryableHTTPStatus(code) {
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

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError && status <= 599
}

func retryDelay(failedAttempts int, hint time.Duration) time.Duration {
	delay := baseRetryDelay
	for attempt := 1; attempt < failedAttempts && delay < maxRetryDelay; attempt++ {
		delay = min(delay*2, maxRetryDelay)
	}

	quarter := delay / 4
	delay += time.Duration(rand.Int64N(int64(quarter)*2+1)) - quarter
	return max(min(delay, maxRetryDelay), min(hint, maxRetryDelay))
}

func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	if milliseconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("retry-after-ms")), 10, 64); err == nil && milliseconds > 0 {
		return time.Duration(min(milliseconds, int64(maxRetryDelay/time.Millisecond))) * time.Millisecond
	}

	value := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Duration(max(0, min(seconds, int64(maxRetryDelay/time.Second)))) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return max(0, when.Sub(now))
}

type transportErrorDisposition struct {
	cause          error
	returnDirectly bool
	retryable      bool
}

func classifyTransportError(ctx context.Context, err error) transportErrorDisposition {
	if contextErr := ctx.Err(); contextErr != nil {
		return transportErrorDisposition{cause: contextErr, returnDirectly: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return transportErrorDisposition{cause: context.DeadlineExceeded, retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return transportErrorDisposition{cause: context.Canceled}
	}
	return transportErrorDisposition{cause: err, retryable: retryableNetworkError(err)}
}

// net/http exposes its internal HTTP/2 stream errors to matching structs through errors.As.
type http2StreamErrorCode uint32

type http2StreamError struct {
	StreamID uint32
	Code     http2StreamErrorCode
	Cause    error
}

func (http2StreamError) Error() string { return "HTTP/2 stream error" }

func retryableNetworkError(err error) bool {
	const http2InternalError http2StreamErrorCode = 2

	var streamErr http2StreamError
	if errors.As(err, &streamErr) && streamErr.Code == http2InternalError {
		return true
	}

	for _, target := range []error{
		io.EOF,
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
