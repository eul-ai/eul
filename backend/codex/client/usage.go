package client

import (
	"context"
	"errors"
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
	return normalizeProviderUsage(wire)
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
