package codex

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/backend/codex/oauth"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const usageHTTPTimeout = 30 * time.Second

type usageResponse struct {
	RateLimit *struct {
		PrimaryWindow   *usageWindow `json:"primary_window"`
		SecondaryWindow *usageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type usageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

func loadUsage(ctx context.Context, manager *oauth.Manager, httpClient *http.Client) (backend.AccountUsage, error) {
	credential, err := resolveCredential(ctx, manager)
	if err != nil {
		return backend.AccountUsage{}, err
	}

	var response usageResponse
	httpClient = backendhttp.CloneNoRedirects(httpClient, usageHTTPTimeout)
	if err := backendhttp.GetJSON(ctx, httpClient, baseURL+"/wham/usage", func(request *http.Request) {
		setCredentialHeaders(request, credential)
	}, &response); err != nil {
		return backend.AccountUsage{}, fmt.Errorf("openai: load usage: %w", err)
	}
	if response.RateLimit == nil {
		return backend.AccountUsage{}, nil
	}

	var windows []backend.UsageWindow
	for _, window := range []*usageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
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
	return backend.AccountUsage{Windows: windows}, nil
}
