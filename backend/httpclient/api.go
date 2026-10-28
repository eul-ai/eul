package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	text := strings.Join(parts, "/")
	if text != "" && detail.Message != "" {
		text += ": "
	}
	text += detail.Message

	provider := formatProviderError(detail.Metadata)
	switch {
	case provider == "":
		return text
	case text == "":
		return provider
	default:
		return text + "; " + provider
	}
}

func formatProviderError(metadata json.RawMessage) string {
	var provider struct {
		Name string          `json:"provider_name"`
		Raw  json.RawMessage `json:"raw"`
	}
	_ = json.Unmarshal(metadata, &provider)

	name := strings.TrimSpace(provider.Name)
	detail := strings.TrimSpace(string(provider.Raw))
	var decoded string
	if json.Unmarshal(provider.Raw, &decoded) == nil {
		detail = strings.TrimSpace(decoded)
	}

	switch {
	case name == "":
		return detail
	case detail == "":
		return name
	default:
		return name + ": " + detail
	}
}

type APIErrorConfig struct {
	Prefix string
}

type APIResponseError struct {
	message    string
	statusCode int
	retryAfter time.Duration
	detail     APIError
	cause      error
}

func (err *APIResponseError) Error() string    { return err.message }
func (err *APIResponseError) Unwrap() error    { return err.cause }
func (err *APIResponseError) StatusCode() int  { return err.statusCode }
func (err *APIResponseError) Detail() APIError { return err.detail }

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
		classified := classifyTransportError(ctx, err)
		if classified.returnDirectly {
			return nil, classified.cause
		}
		if classified.retryable {
			return nil, config.retryableWrapf(classified.cause, "%s failed: %v", operation, err)
		}
		return nil, config.Wrapf(classified.cause, "%s failed: %v", operation, err)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	return nil, config.decodeAPIResponseError(response)
}

func (config APIErrorConfig) decodeAPIResponseError(response *http.Response) error {
	body, truncated, err := ReadBounded(response.Body, maxErrorBytes)
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
			if formatted := FormatAPIError(envelope.Error); formatted != "" {
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

func (config APIErrorConfig) retryableWrapf(cause error, format string, arguments ...any) error {
	return &retryableOperationError{cause: config.Wrapf(cause, format, arguments...)}
}

func (config APIErrorConfig) message(format string, arguments ...any) string {
	return FormatErrorMessage(config.Prefix, format, arguments...)
}
