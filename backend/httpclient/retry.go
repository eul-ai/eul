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

func ParseRetryAfter(value string, now time.Time) time.Duration {
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

func RetryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError && status <= 599
}

func RetryDelay(failedAttempts int, base, maximum time.Duration) time.Duration {
	delay := base
	for attempt := 1; attempt < failedAttempts && delay < maximum; attempt++ {
		delay = min(delay*2, maximum)
	}

	quarter := delay / 4
	if quarter == 0 {
		return delay
	}
	delay += time.Duration(rand.Int64N(int64(quarter)*2+1)) - quarter
	return min(delay, maximum)
}

func RetryDelayWithHint(failedAttempts int, base, maximum, hint time.Duration) time.Duration {
	delay := RetryDelay(failedAttempts, base, maximum)
	if hint > delay {
		return min(hint, maximum)
	}
	return delay
}

type TransportErrorDisposition struct {
	Cause          error
	ReturnDirectly bool
	Retryable      bool
}

func ClassifyTransportError(ctx context.Context, err error) TransportErrorDisposition {
	if contextErr := ctx.Err(); contextErr != nil {
		return TransportErrorDisposition{Cause: contextErr, ReturnDirectly: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TransportErrorDisposition{Cause: context.DeadlineExceeded, Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return TransportErrorDisposition{Cause: context.Canceled}
	}
	return TransportErrorDisposition{Cause: err, Retryable: RetryableNetworkError(err)}
}

// net/http exposes its internal HTTP/2 stream errors to matching structs through errors.As.
type http2StreamErrorCode uint32

type http2StreamError struct {
	StreamID uint32
	Code     http2StreamErrorCode
	Cause    error
}

func (http2StreamError) Error() string { return "HTTP/2 stream error" }

func RetryableNetworkError(err error) bool {
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
