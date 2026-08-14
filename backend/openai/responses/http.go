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
)

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
