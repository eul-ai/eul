package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/oauth"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultUsageBaseURL          = "https://chatgpt.com/backend-api"
	defaultUsageHTTPTimeout      = 10 * time.Minute
	defaultMaxUsageResponseBytes = int64(16 * 1024 * 1024)
	defaultMaxUsageErrorBytes    = int64(64 * 1024)
)

type usageClientOptions struct {
	httpClient *http.Client
	baseURL    string
}

type usageClient struct {
	httpClient       *http.Client
	endpoint         string
	manager          oauthManager
	maxResponseBytes int64
	maxErrorBytes    int64
}

type usageResponse struct {
	RateLimit *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *providerUsageWindow `json:"primary_window"`
	SecondaryWindow *providerUsageWindow `json:"secondary_window"`
}

type providerUsageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

func newUsageClient(manager oauthManager, options usageClientOptions) (*usageClient, error) {
	baseURL := options.baseURL
	if baseURL == "" {
		baseURL = defaultUsageBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: parse base URL: %w", err)
	}

	endpoint := baseURL + "/api/codex/usage"
	if strings.HasSuffix(strings.TrimRight(parsedBaseURL.Path, "/"), "/backend-api") {
		endpoint = baseURL + "/wham/usage"
	}
	return &usageClient{
		httpClient:       backendhttp.CloneNoRedirects(options.httpClient, defaultUsageHTTPTimeout),
		endpoint:         endpoint,
		manager:          manager,
		maxResponseBytes: defaultMaxUsageResponseBytes,
		maxErrorBytes:    defaultMaxUsageErrorBytes,
	}, nil
}

func (client *usageClient) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if err := ctx.Err(); err != nil {
		return backend.AccountUsage{}, err
	}

	credential, err := client.resolveCredential(ctx)
	if err != nil {
		return backend.AccountUsage{}, err
	}
	response, err := client.get(ctx, credential)
	if err != nil {
		return backend.AccountUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := backendhttp.ReadBounded(response.Body, client.maxResponseBytes)
	if err != nil {
		if classified := client.contextError(ctx, err, "read usage response"); classified != nil {
			return backend.AccountUsage{}, classified
		}
		return backend.AccountUsage{}, client.errorf("read usage response: %v", err)
	}
	if truncated {
		return backend.AccountUsage{}, client.errorf("usage response exceeds %d bytes", client.maxResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return backend.AccountUsage{}, client.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func (client *usageClient) get(ctx context.Context, credential oauth.AccessCredential) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
	if err != nil {
		return nil, client.errorf("create usage request: %v", err)
	}
	setUsageCredentialHeaders(request, credential)
	request.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, client.wrapf(err, "usage request failed: %v", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _, readErr := backendhttp.ReadBounded(response.Body, client.maxErrorBytes)
		if readErr != nil {
			return nil, client.wrapf(readErr, "HTTP %s; read error response: %v", response.Status, readErr)
		}
		return nil, client.errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func setUsageCredentialHeaders(request *http.Request, credential oauth.AccessCredential) {
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
}

func (client *usageClient) contextError(ctx context.Context, err error, operation string) error {
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

func (client *usageClient) resolveCredential(ctx context.Context) (oauth.AccessCredential, error) {
	credential, err := client.manager.Resolve(ctx)
	if err == nil {
		return credential, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return oauth.AccessCredential{}, contextErr
	}
	return oauth.AccessCredential{}, client.wrapf(err, "resolve authentication: %v", err)
}

func (client *usageClient) errorf(format string, arguments ...any) error {
	return errors.New(client.errorMessage(format, arguments...))
}

func (client *usageClient) wrapf(cause error, format string, arguments ...any) error {
	return &usageWrappedError{message: client.errorMessage(format, arguments...), cause: cause}
}

func (client *usageClient) errorMessage(format string, arguments ...any) string {
	return backendhttp.FormatErrorMessage("openai", client.maxErrorBytes, format, arguments...)
}

type usageWrappedError struct {
	message string
	cause   error
}

func (err *usageWrappedError) Error() string { return err.message }
func (err *usageWrappedError) Unwrap() error { return err.cause }

func normalizeProviderUsage(response usageResponse) (backend.AccountUsage, error) {
	if response.RateLimit == nil {
		return backend.AccountUsage{}, nil
	}

	windows := make([]backend.UsageWindow, 0, 2)
	for _, window := range []*providerUsageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if window.UsedPercent < 0 || window.UsedPercent > 100 {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid used percentage")
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if window.LimitWindowSeconds <= 0 || window.LimitWindowSeconds > maximumSeconds {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid window duration")
		}
		if window.ResetAt < 0 {
			return backend.AccountUsage{}, errors.New("openai: usage response contains an invalid reset time")
		}

		usageWindow := backend.UsageWindow{
			Duration:    time.Duration(window.LimitWindowSeconds) * time.Second,
			UsedPercent: window.UsedPercent,
		}
		if window.ResetAt > 0 {
			usageWindow.ResetsAt = time.Unix(window.ResetAt, 0)
		}
		windows = append(windows, usageWindow)
	}

	return backend.AccountUsage{Windows: windows}, nil
}
