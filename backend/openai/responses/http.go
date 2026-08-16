package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

func (c *Client) post(ctx context.Context, body []byte, operation string) (*http.Response, error) {
	request, err := backendhttp.NewJSONSSERequest(ctx, c.endpoint, body)
	if err != nil {
		return nil, c.errorf("create %s: %v", operation, err)
	}
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
		classified := backendhttp.ClassifyTransportError(ctx, err)
		if classified.ReturnDirectly {
			return nil, classified.Cause
		}
		if classified.Retryable {
			return nil, c.retryableWrapf(classified.Cause, "%s failed: %v", operation, err)
		}
		return nil, c.wrapf(classified.Cause, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, c.decodeHTTPError(response)
	}
	return response, nil
}

func (c *Client) decodeHTTPError(response *http.Response) error {
	body, truncated, err := backendhttp.ReadBounded(response.Body, c.maxErrorBytes)
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
	return &httpResponseError{
		message:    c.errorMessage(format, arguments...),
		statusCode: response.StatusCode,
		retryAfter: backendhttp.ParseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
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
	return backendhttp.FormatErrorMessage(c.errorPrefix, c.maxErrorBytes, format, arguments...)
}
