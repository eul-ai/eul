package messages

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type apiError = backendhttp.APIError

type responseFailureError struct {
	message string
	detail  apiError
}

func (err *responseFailureError) Error() string { return err.message }

func parseRetryAfterHeaders(headers http.Header, now time.Time) time.Duration {
	if milliseconds, err := strconv.ParseInt(strings.TrimSpace(headers.Get("retry-after-ms")), 10, 64); err == nil && milliseconds > 0 {
		maximum := int64(time.Duration(1<<63-1) / time.Millisecond)
		if milliseconds > maximum {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(milliseconds) * time.Millisecond
	}
	return backendhttp.ParseRetryAfter(headers.Get("Retry-After"), now)
}

func (client *Client) errorf(format string, arguments ...any) error {
	return client.errorConfig.Errorf(format, arguments...)
}

func (client *Client) wrapf(cause error, format string, arguments ...any) error {
	return client.errorConfig.Wrapf(cause, format, arguments...)
}
