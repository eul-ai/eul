package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type errorCode string

func (code *errorCode) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*code = ""
		return nil
	}

	var text string
	if json.Unmarshal(data, &text) == nil {
		*code = errorCode(text)
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*code = errorCode(number.String())
	return nil
}

type apiError struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
	Type    string    `json:"type"`
}

type responseFailureError struct {
	message string
	detail  apiError
}

func (err *responseFailureError) Error() string { return err.message }

type httpResponseError struct {
	message    string
	statusCode int
	retryAfter time.Duration
	detail     apiError
	cause      error
}

func (err *httpResponseError) Error() string { return err.message }
func (err *httpResponseError) Unwrap() error { return err.cause }

type wrappedError struct {
	message string
	cause   error
}

func (err *wrappedError) Error() string { return err.message }
func (err *wrappedError) Unwrap() error { return err.cause }

type retryableOperationError struct {
	cause error
}

func (err *retryableOperationError) Error() string { return err.cause.Error() }
func (err *retryableOperationError) Unwrap() error { return err.cause }

func (client *Client) post(ctx context.Context, body []byte, operation string) (*http.Response, error) {
	request, err := backendhttp.NewJSONSSERequest(ctx, client.endpoint, body)
	if err != nil {
		return nil, client.errorf("create %s: %v", operation, err)
	}
	if client.prepareRequest != nil {
		if err := client.prepareRequest(ctx, request); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, err
		}
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		classified := backendhttp.ClassifyTransportError(ctx, err)
		if classified.ReturnDirectly {
			return nil, classified.Cause
		}
		if classified.Retryable {
			return nil, client.retryableWrapf(classified.Cause, "%s failed: %v", operation, err)
		}
		return nil, client.wrapf(classified.Cause, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, client.decodeHTTPError(response)
	}
	return response, nil
}

func (client *Client) decodeHTTPError(response *http.Response) error {
	body, truncated, err := backendhttp.ReadBounded(response.Body, client.maxErrorBytes)
	if err != nil {
		return client.newHTTPResponseError(response, err, apiError{}, "HTTP %s; read error response: %v", response.Status, err)
	}

	detail := strings.TrimSpace(string(body))
	var errorDetail apiError
	if !truncated {
		var envelope struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if formatted := formatAPIError(envelope.Error); formatted != "" {
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
	return client.newHTTPResponseError(response, nil, errorDetail, "HTTP %s: %s", response.Status, detail)
}

func (client *Client) newHTTPResponseError(response *http.Response, cause error, detail apiError, format string, arguments ...any) error {
	return &httpResponseError{
		message:    client.errorMessage(format, arguments...),
		statusCode: response.StatusCode,
		retryAfter: backendhttp.ParseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		detail:     detail,
		cause:      cause,
	}
}

func (client *Client) errorf(format string, arguments ...any) error {
	return errors.New(client.errorMessage(format, arguments...))
}

func (client *Client) wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: client.errorMessage(format, arguments...), cause: cause}
}

func (client *Client) retryableWrapf(cause error, format string, arguments ...any) error {
	return &retryableOperationError{cause: client.wrapf(cause, format, arguments...)}
}

func (client *Client) errorMessage(format string, arguments ...any) string {
	return backendhttp.FormatErrorMessage(client.errorPrefix, client.maxErrorBytes, format, arguments...)
}

func formatAPIError(detail apiError) string {
	parts := make([]string, 0, 2)
	if detail.Type != "" {
		parts = append(parts, detail.Type)
	}
	if detail.Code != "" {
		parts = append(parts, string(detail.Code))
	}
	prefix := strings.Join(parts, "/")
	if prefix == "" {
		return detail.Message
	}
	if detail.Message == "" {
		return prefix
	}
	return prefix + ": " + detail.Message
}
