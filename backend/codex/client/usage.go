package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type UsageOptions struct {
	HTTPClient *http.Client
	BaseURL    string
}

type UsageClient struct {
	requests         *requestClient
	usageEndpoint    string
	maxResponseBytes int64
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

func NewUsage(source TokenSource, options UsageOptions) (*UsageClient, error) {
	requests, err := newRequestClient(source, options.HTTPClient)
	if err != nil {
		return nil, err
	}
	baseURL, parsedBaseURL, err := normalizeBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}

	usageEndpoint := baseURL + "/api/codex/usage"
	if strings.HasSuffix(strings.TrimRight(parsedBaseURL.Path, "/"), "/backend-api") {
		usageEndpoint = baseURL + "/wham/usage"
	}
	return &UsageClient{
		requests:         requests,
		usageEndpoint:    usageEndpoint,
		maxResponseBytes: backendhttp.DefaultResponseBytes,
	}, nil
}

func (client *UsageClient) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if err := ctx.Err(); err != nil {
		return backend.AccountUsage{}, err
	}

	credential, err := client.requests.resolveCredential(ctx)
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
		if classified := client.requests.contextError(ctx, err, "read usage response"); classified != nil {
			return backend.AccountUsage{}, classified
		}
		return backend.AccountUsage{}, client.requests.errorf("read usage response: %v", err)
	}
	if truncated {
		return backend.AccountUsage{}, client.requests.errorf("usage response exceeds %d bytes", client.maxResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return backend.AccountUsage{}, client.requests.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func (client *UsageClient) get(ctx context.Context, credential Credential) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.usageEndpoint, nil)
	if err != nil {
		return nil, client.requests.errorf("create usage request: %v", err)
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("Accept", "application/json")

	response, err := client.requests.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, client.requests.wrapf(err, "usage request failed: %v", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, _, readErr := backendhttp.ReadBounded(response.Body, client.requests.maxErrorBytes)
		if readErr != nil {
			return nil, client.requests.wrapf(readErr, "HTTP %s; read error response: %v", response.Status, readErr)
		}
		return nil, client.requests.errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

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

var _ backend.UsageProvider = (*UsageClient)(nil)
