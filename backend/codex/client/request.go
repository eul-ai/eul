package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const defaultBaseURL = "https://chatgpt.com/backend-api"

type Credential struct {
	AccessToken string
	AccountID   string
}

type TokenSource interface {
	Token(context.Context) (Credential, error)
}

type requestClient struct {
	tokenSource   TokenSource
	httpClient    *http.Client
	maxErrorBytes int64
}

func newRequestClient(source TokenSource, httpClient *http.Client) (*requestClient, error) {
	if source == nil {
		return nil, errors.New("openai: token source is required")
	}
	return &requestClient{
		tokenSource:   source,
		httpClient:    backendhttp.CloneNoRedirects(httpClient, backendhttp.DefaultGenerationHTTPTimeout),
		maxErrorBytes: backendhttp.DefaultErrorResponseBytes,
	}, nil
}

func normalizeBaseURL(value string) (string, *url.URL, error) {
	if value == "" {
		value = defaultBaseURL
	}
	value = strings.TrimRight(value, "/")
	parsed, err := url.Parse(value)
	if err != nil {
		return "", nil, fmt.Errorf("openai: parse base URL: %w", err)
	}
	return value, parsed, nil
}

func (client *requestClient) authenticate(ctx context.Context, request *http.Request) error {
	credential, err := client.resolveCredential(ctx)
	if err != nil {
		return err
	}
	setCredentialHeaders(request, credential)
	return nil
}

func setCredentialHeaders(request *http.Request, credential Credential) {
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
}

func (client *requestClient) resolveCredential(ctx context.Context) (Credential, error) {
	credential, err := client.tokenSource.Token(ctx)
	if err == nil {
		return credential, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return Credential{}, contextErr
	}
	return Credential{}, client.wrapf(err, "resolve authentication: %v", err)
}

func (client *requestClient) contextError(ctx context.Context, err error, operation string) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return client.wrapf(context.DeadlineExceeded, "%s: %v", operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return client.wrapf(context.Canceled, "%s: %v", operation, err)
	}
	return nil
}

func (client *requestClient) errorf(format string, arguments ...any) error {
	return errors.New(client.errorMessage(format, arguments...))
}

func (client *requestClient) wrapf(cause error, format string, arguments ...any) error {
	return &wrappedError{message: client.errorMessage(format, arguments...), cause: cause}
}

func (client *requestClient) errorMessage(format string, arguments ...any) string {
	return backendhttp.FormatErrorMessage("openai", client.maxErrorBytes, format, arguments...)
}

type wrappedError struct {
	message string
	cause   error
}

func (err *wrappedError) Error() string { return err.message }
func (err *wrappedError) Unwrap() error { return err.cause }
