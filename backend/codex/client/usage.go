package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type UsageClient struct {
	requests *requestClient
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

func NewUsage(source TokenSource) (*UsageClient, error) {
	return newUsage(source, nil)
}

func newUsage(source TokenSource, httpClient *http.Client) (*UsageClient, error) {
	requests, err := newRequestClient(source, httpClient)
	if err != nil {
		return nil, err
	}
	return &UsageClient{requests: requests}, nil
}

func (client *UsageClient) Usage(ctx context.Context) (backend.AccountUsage, error) {
	if err := ctx.Err(); err != nil {
		return backend.AccountUsage{}, err
	}

	credential, err := client.requests.resolveCredential(ctx)
	if err != nil {
		return backend.AccountUsage{}, err
	}

	var wire usageResponse
	if err := backendhttp.GetJSON(ctx, client.requests.httpClient, baseURL+"/wham/usage", func(request *http.Request) {
		setCredentialHeaders(request, credential)
	}, &wire); err != nil {
		return backend.AccountUsage{}, fmt.Errorf("openai: load usage: %w", err)
	}
	return normalizeProviderUsage(wire), nil
}

func normalizeProviderUsage(response usageResponse) backend.AccountUsage {
	if response.RateLimit == nil {
		return backend.AccountUsage{}
	}

	windows := make([]backend.UsageWindow, 0, 2)
	for _, window := range []*providerUsageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
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

	return backend.AccountUsage{Windows: windows}
}

var _ backend.UsageProvider = (*UsageClient)(nil)
