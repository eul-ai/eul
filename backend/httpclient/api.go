package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type APIErrorCode string

func (code *APIErrorCode) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*code = ""
		return nil
	}

	var text string
	if json.Unmarshal(data, &text) == nil {
		*code = APIErrorCode(text)
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*code = APIErrorCode(number.String())
	return nil
}

type APIError struct {
	Type     string          `json:"type"`
	Code     APIErrorCode    `json:"code"`
	Message  string          `json:"message"`
	Metadata json.RawMessage `json:"metadata"`
}

func FormatAPIError(detail APIError) string {
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

func IsContextLimitAPIError(detail APIError) bool {
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

func IsRetryableAPIError(detail APIError) bool {
	if code, err := strconv.Atoi(string(detail.Code)); err == nil && RetryableHTTPStatus(code) {
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

type APIErrorConfig struct {
	Prefix          string
	Maximum         int64
	ParseRetryAfter func(http.Header, time.Time) time.Duration
	FormatDetail    func(APIError) string
}

type APIResponseError struct {
	message    string
	statusCode int
	retryAfter time.Duration
	detail     APIError
	cause      error
}

func (err *APIResponseError) Error() string             { return err.message }
func (err *APIResponseError) Unwrap() error             { return err.cause }
func (err *APIResponseError) StatusCode() int           { return err.statusCode }
func (err *APIResponseError) RetryAfter() time.Duration { return err.retryAfter }
func (err *APIResponseError) Detail() APIError          { return err.detail }

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

func (config APIErrorConfig) Do(ctx context.Context, client *http.Client, request *http.Request, operation string) (*http.Response, error) {
	response, err := client.Do(request)
	if err != nil {
		classified := ClassifyTransportError(ctx, err)
		if classified.ReturnDirectly {
			return nil, classified.Cause
		}
		if classified.Retryable {
			return nil, config.RetryableWrapf(classified.Cause, "%s failed: %v", operation, err)
		}
		return nil, config.Wrapf(classified.Cause, "%s failed: %v", operation, err)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	return nil, config.decodeAPIResponseError(response)
}

func (config APIErrorConfig) decodeAPIResponseError(response *http.Response) error {
	body, truncated, err := ReadBounded(response.Body, config.Maximum)
	if err != nil {
		return config.responseError(response, err, APIError{}, "HTTP %s; read error response: %v", response.Status, err)
	}

	detailText := strings.TrimSpace(string(body))
	var detail APIError
	if !truncated {
		var envelope struct {
			Error APIError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			formatDetail := config.FormatDetail
			if formatDetail == nil {
				formatDetail = FormatAPIError
			}
			if formatted := formatDetail(envelope.Error); formatted != "" {
				detail = envelope.Error
				detailText = formatted
			}
		}
	}
	if detailText == "" {
		detailText = "empty error response"
	}
	if truncated {
		detailText += " [truncated]"
	}
	return config.responseError(response, nil, detail, "HTTP %s: %s", response.Status, detailText)
}

func (config APIErrorConfig) responseError(response *http.Response, cause error, detail APIError, format string, arguments ...any) error {
	parseRetryAfter := config.ParseRetryAfter
	if parseRetryAfter == nil {
		parseRetryAfter = func(headers http.Header, now time.Time) time.Duration {
			return ParseRetryAfter(headers.Get("Retry-After"), now)
		}
	}
	return &APIResponseError{
		message:    config.message(format, arguments...),
		statusCode: response.StatusCode,
		retryAfter: parseRetryAfter(response.Header, time.Now()),
		detail:     detail,
		cause:      cause,
	}
}

func (config APIErrorConfig) Errorf(format string, arguments ...any) error {
	return errors.New(config.message(format, arguments...))
}

func (config APIErrorConfig) Wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: config.message(format, arguments...), cause: cause}
}

func (config APIErrorConfig) RetryableWrapf(cause error, format string, arguments ...any) error {
	return &retryableOperationError{cause: config.Wrapf(cause, format, arguments...)}
}

func (config APIErrorConfig) message(format string, arguments ...any) string {
	return FormatErrorMessage(config.Prefix, config.Maximum, format, arguments...)
}

func IsRetryableOperation(err error) bool {
	var retryable *retryableOperationError
	return errors.As(err, &retryable)
}

type RetryPolicy struct {
	MaximumAttempts int
	BaseDelay       time.Duration
	MaximumDelay    time.Duration
}

func (policy RetryPolicy) Next(err error, failedAttempts int, retryable bool) (time.Duration, bool) {
	if failedAttempts >= policy.MaximumAttempts || !retryable {
		return 0, false
	}

	var retryAfter time.Duration
	var hinted interface{ RetryAfter() time.Duration }
	if errors.As(err, &hinted) {
		retryAfter = hinted.RetryAfter()
	}
	return RetryDelayWithHint(failedAttempts, policy.BaseDelay, policy.MaximumDelay, retryAfter), true
}
