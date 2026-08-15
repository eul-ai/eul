package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, client.errorf("create %s: %v", operation, err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
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
		if classified := client.contextError(ctx, err, operation+" failed"); classified != nil {
			return nil, classified
		}
		if backendhttp.RetryableNetworkError(err) {
			return nil, client.retryableWrapf(err, "%s failed: %v", operation, err)
		}
		return nil, client.wrapf(err, "%s failed: %v", operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, client.decodeHTTPError(response)
	}
	return response, nil
}

func (client *Client) contextError(ctx context.Context, err error, operation string) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return client.retryableWrapf(context.DeadlineExceeded, "%s: %v", operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return client.wrapf(context.Canceled, "%s: %v", operation, err)
	}
	return nil
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
	detail.Message = client.redactMessage(detail.Message)
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
	message := client.redactMessage(strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�"))
	if client.errorPrefix != "" {
		message = client.errorPrefix + ": " + message
	}
	return backendhttp.TruncateUTF8(message, int(client.maxErrorBytes))
}

func (client *Client) redactMessage(message string) string {
	return backendhttp.Redact(message, client.redact)
}

func (client *Client) redactResponseFailure(err error) {
	var responseErr *responseFailureError
	if !errors.As(err, &responseErr) {
		return
	}
	responseErr.message = client.redactMessage(responseErr.message)
	responseErr.detail.Message = client.redactMessage(responseErr.detail.Message)
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
