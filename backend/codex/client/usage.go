package client

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

type AccountUsage struct {
	Windows []UsageWindow
}

type UsageWindow struct {
	Duration    time.Duration
	UsedPercent int
	ResetsAt    time.Time
}

type usageResponse struct {
	RateLimit *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

func (c *Client) Usage(ctx context.Context) (AccountUsage, error) {
	if err := ctx.Err(); err != nil {
		return AccountUsage{}, err
	}

	credential, err := c.resolveCredential(ctx)
	if err != nil {
		return AccountUsage{}, err
	}
	response, err := c.get(ctx, c.usageEndpoint, credential, "usage request")
	if err != nil {
		return AccountUsage{}, err
	}
	defer response.Body.Close()

	body, truncated, err := backendhttp.ReadBounded(response.Body, c.maxUsageResponseBytes)
	if err != nil {
		if classified := c.contextError(ctx, err, "read usage response"); classified != nil {
			return AccountUsage{}, classified
		}
		return AccountUsage{}, c.errorf("read usage response: %v", err)
	}
	if truncated {
		return AccountUsage{}, c.errorf("usage response exceeds %d bytes", c.maxUsageResponseBytes)
	}

	var wire usageResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return AccountUsage{}, c.errorf("decode usage response: %v", err)
	}
	return normalizeProviderUsage(wire)
}

func normalizeProviderUsage(response usageResponse) (AccountUsage, error) {
	if response.RateLimit == nil {
		return AccountUsage{}, nil
	}

	windows := make([]UsageWindow, 0, 2)
	for _, window := range []*usageWindow{response.RateLimit.PrimaryWindow, response.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if window.UsedPercent < 0 || window.UsedPercent > 100 {
			return AccountUsage{}, errors.New("openai: usage response contains an invalid used percentage")
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if window.LimitWindowSeconds <= 0 || window.LimitWindowSeconds > maximumSeconds {
			return AccountUsage{}, errors.New("openai: usage response contains an invalid window duration")
		}
		if window.ResetAt < 0 {
			return AccountUsage{}, errors.New("openai: usage response contains an invalid reset time")
		}

		usageWindow := UsageWindow{
			Duration:    time.Duration(window.LimitWindowSeconds) * time.Second,
			UsedPercent: window.UsedPercent,
		}
		if window.ResetAt > 0 {
			usageWindow.ResetsAt = time.Unix(window.ResetAt, 0)
		}
		windows = append(windows, usageWindow)
	}

	if len(windows) == 0 {
		return AccountUsage{}, nil
	}
	return AccountUsage{Windows: windows}, nil
}
