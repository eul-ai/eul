package httpclient

import (
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

func New(source *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{}
	if source != nil {
		*client = *source
	}
	if client.Timeout <= 0 {
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

func MarshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	return body, int64(len(body)) > maximum, nil
}

func ReadBounded(reader io.Reader, maximum int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= maximum {
		return data, false, nil
	}
	return data[:maximum], true, nil
}

func TruncateUTF8(text string, maximum int) string {
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

func Redact(message string, values []string) string {
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}

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
