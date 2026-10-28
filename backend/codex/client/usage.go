package client

import (
	"context"
	"encoding/json"
	"errors"
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
	response, err := client.get(ctx, credential)
	if err != nil {
		return backend.AccountUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := backendhttp.ReadBounded(response.Body, backendhttp.MaxResponseBytes)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return backend.AccountUsage{}, contextErr
		}
		return backend.AccountUsage{}, openAIErrors.Wrapf(err, "read usage response: %v", err)
	}
	if truncated {
		return backend.AccountUsage{}, openAIErrors.Errorf("usage response exceeds %d bytes", backendhttp.MaxResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return backend.AccountUsage{}, openAIErrors.Errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func (client *UsageClient) get(ctx context.Context, credential Credential) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/wham/usage", nil)
	if err != nil {
		return nil, openAIErrors.Errorf("create usage request: %v", err)
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("Accept", "application/json")

	response, err := client.requests.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, openAIErrors.Wrapf(err, "usage request failed: %v", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		err := backendhttp.ReadHTTPStatusError(response)
		return nil, openAIErrors.Wrapf(err, "%v", err)
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
